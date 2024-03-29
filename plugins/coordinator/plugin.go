package coordinator

import (
	"errors"
	"sync"
	"time"

	"github.com/spf13/pflag"

	"github.com/iotaledger/hive.go/daemon"
	"github.com/iotaledger/hive.go/events"
	"github.com/iotaledger/hive.go/logger"
	"github.com/iotaledger/hive.go/node"
	"github.com/iotaledger/hive.go/syncutils"
	"github.com/iotaledger/hive.go/timeutil"
	"github.com/iotaledger/iota.go/consts"
	"github.com/iotaledger/iota.go/transaction"

	"github.com/Ariwonto/aingle-alpha/pkg/config"
	"github.com/Ariwonto/aingle-alpha/pkg/dag"
	"github.com/Ariwonto/aingle-alpha/pkg/model/coordinator"
	"github.com/Ariwonto/aingle-alpha/pkg/model/aingle"
	"github.com/Ariwonto/aingle-alpha/pkg/model/milestone"
	"github.com/Ariwonto/aingle-alpha/pkg/model/mselection"
	"github.com/Ariwonto/aingle-alpha/pkg/model/tangle"
	powpackage "github.com/Ariwonto/aingle-alpha/pkg/pow"
	"github.com/Ariwonto/aingle-alpha/pkg/shutdown"
	"github.com/Ariwonto/aingle-alpha/pkg/whiteflag"
	"github.com/Ariwonto/aingle-alpha/plugins/gossip"
	"github.com/Ariwonto/aingle-alpha/plugins/pow"
	tangleplugin "github.com/Ariwonto/aingle-alpha/plugins/tangle"
)

func init() {
	pflag.CommandLine.MarkHidden("cooBootstrap")
	pflag.CommandLine.MarkHidden("cooStartIndex")
}

var (
	PLUGIN = node.NewPlugin("Coordinator", node.Disabled, configure, run)
	log    *logger.Logger

	bootstrap  = pflag.Bool("cooBootstrap", false, "bootstrap the network")
	startIndex = pflag.Uint32("cooStartIndex", 0, "index of the first milestone at bootstrap")

	maxTrackedTails int
	belowMaxDepth   milestone.Index

	nextCheckpointSignal chan struct{}
	nextMilestoneSignal  chan struct{}

	coo      *coordinator.Coordinator
	selector *mselection.HeaviestSelector

	lastCheckpointIndex int
	lastCheckpointHash  aingle.Hash
	lastMilestoneHash   aingle.Hash

	// Closures
	onBundleSolid                 *events.Closure
	onMilestoneConfirmed          *events.Closure
	onIssuedCheckpointTransaction *events.Closure
	onIssuedMilestone             *events.Closure

	ErrDatabaseTainted = errors.New("database is tainted. delete the coordinator database and start again with a local snapshot")
)

func configure(plugin *node.Plugin) {
	log = logger.NewLogger(plugin.Name)

	// set the node as synced at startup, so the coo plugin can select tips
	tangleplugin.SetUpdateSyncedAtStartup(true)

	var err error
	coo, err = initCoordinator(*bootstrap, *startIndex, pow.Handler())
	if err != nil {
		log.Panic(err)
	}

	configureEvents()
}

func initCoordinator(bootstrap bool, startIndex uint32, powHandler *powpackage.Handler) (*coordinator.Coordinator, error) {

	if tangle.IsDatabaseTainted() {
		return nil, ErrDatabaseTainted
	}

	seed, err := config.LoadHashFromEnvironment("COO_SEED")
	if err != nil {
		return nil, err
	}

	// use the heaviest branch tip selection for the milestones
	selector = mselection.New(
		config.NodeConfig.GetInt(config.CfgCoordinatorTipselectMinHeaviestBranchUnconfirmedTransactionsThreshold),
		config.NodeConfig.GetInt(config.CfgCoordinatorTipselectMaxHeaviestBranchTipsPerCheckpoint),
		config.NodeConfig.GetInt(config.CfgCoordinatorTipselectRandomTipsPerCheckpoint),
		time.Duration(config.NodeConfig.GetInt(config.CfgCoordinatorTipselectHeaviestBranchSelectionDeadlineMilliseconds))*time.Millisecond,
	)

	nextCheckpointSignal = make(chan struct{})

	// must be a buffered channel, otherwise signal gets
	// lost if checkpoint is generated at the same time
	nextMilestoneSignal = make(chan struct{}, 1)

	maxTrackedTails = config.NodeConfig.GetInt(config.CfgCoordinatorCheckpointsMaxTrackedTails)

	belowMaxDepth = milestone.Index(config.NodeConfig.GetInt(config.CfgTipSelBelowMaxDepth))

	coo := coordinator.New(
		seed,
		consts.SecurityLevel(config.NodeConfig.GetInt(config.CfgCoordinatorSecurityLevel)),
		config.NodeConfig.GetInt(config.CfgCoordinatorMerkleTreeDepth),
		config.NodeConfig.GetInt(config.CfgCoordinatorMWM),
		config.NodeConfig.GetString(config.CfgCoordinatorStateFilePath),
		config.NodeConfig.GetInt(config.CfgCoordinatorIntervalSeconds),
		powHandler,
		sendBundle,
		coordinator.MilestoneMerkleTreeHashFuncWithName(config.NodeConfig.GetString(config.CfgCoordinatorMilestoneMerkleTreeHashFunc)),
	)

	if err := coo.InitMerkleTree(config.NodeConfig.GetString(config.CfgCoordinatorMerkleTreeFilePath), config.NodeConfig.GetString(config.CfgCoordinatorAddress)); err != nil {
		return nil, err
	}

	if err := coo.InitState(bootstrap, milestone.Index(startIndex)); err != nil {
		return nil, err
	}

	return coo, nil
}

func run(plugin *node.Plugin) {

	// create a background worker that signals to issue new milestones
	daemon.BackgroundWorker("Coordinator[MilestoneTicker]", func(shutdownSignal <-chan struct{}) {

		timeutil.Ticker(func() {
			// issue next milestone
			select {
			case nextMilestoneSignal <- struct{}{}:
			default:
				// do not block if already another signal is waiting
			}
		}, coo.GetInterval(), shutdownSignal)

	}, shutdown.PriorityCoordinator)

	// create a background worker that issues milestones
	daemon.BackgroundWorker("Coordinator", func(shutdownSignal <-chan struct{}) {
		attachEvents()

		// bootstrap the network if not done yet
		milestoneHash, criticalErr := coo.Bootstrap()
		if criticalErr != nil {
			log.Panic(criticalErr)
		}

		// init the last milestone hash
		lastMilestoneHash = milestoneHash

		// init the checkpoints
		lastCheckpointHash = milestoneHash
		lastCheckpointIndex = 0

	coordinatorLoop:
		for {
			select {
			case <-nextCheckpointSignal:
				// check the thresholds again, because a new milestone could have been issued in the meantime
				if trackedTailsCount := selector.GetTrackedTailsCount(); trackedTailsCount < maxTrackedTails {
					continue
				}

				tips, err := selector.SelectTips(0)
				if err != nil {
					// issuing checkpoint failed => not critical
					if err != mselection.ErrNoTipsAvailable {
						log.Warn(err)
					}
					continue
				}

				// issue a checkpoint
				checkpointHash, err := coo.IssueCheckpoint(lastCheckpointIndex, lastCheckpointHash, tips)
				if err != nil {
					// issuing checkpoint failed => not critical
					log.Warn(err)
					continue
				}
				lastCheckpointIndex++
				lastCheckpointHash = checkpointHash

			case <-nextMilestoneSignal:

				// issue a new checkpoint right in front of the milestone
				tips, err := selector.SelectTips(1)
				if err != nil {
					// issuing checkpoint failed => not critical
					if err != mselection.ErrNoTipsAvailable {
						log.Warn(err)
					}
				} else {
					checkpointHash, err := coo.IssueCheckpoint(lastCheckpointIndex, lastCheckpointHash, tips)
					if err != nil {
						// issuing checkpoint failed => not critical
						log.Warn(err)
					} else {
						// use the new checkpoint hash
						lastCheckpointHash = checkpointHash
					}
				}

				milestoneHash, err, criticalErr := coo.IssueMilestone(lastMilestoneHash, lastCheckpointHash)
				if criticalErr != nil {
					log.Panic(criticalErr)
				}
				if err != nil {
					if err == tangle.ErrNodeNotSynced {
						// Coordinator is not synchronized, trigger the solidifier manually
						tangleplugin.TriggerSolidifier()
					}
					log.Warn(err)
					continue
				}

				// remember the last milestone hash
				lastMilestoneHash = milestoneHash

				// reset the checkpoints
				lastCheckpointHash = milestoneHash
				lastCheckpointIndex = 0

			case <-shutdownSignal:
				break coordinatorLoop
			}
		}

		detachEvents()
	}, shutdown.PriorityCoordinator)

}

func sendBundle(b coordinator.Bundle, isMilestone bool) error {

	// collect all tx hashes of the bundle
	txHashes := make(map[string]struct{})
	for _, t := range b {
		txHashes[string(aingle.HashFromHashTrytes(t.Hash))] = struct{}{}
	}

	txHashesLock := syncutils.Mutex{}

	// wgBundleProcessed waits until all txs of the bundle were processed in the storage layer
	wgBundleProcessed := sync.WaitGroup{}
	wgBundleProcessed.Add(len(txHashes))

	if isMilestone {
		// also wait for solid milestone changed event
		wgBundleProcessed.Add(1)
	}

	onProcessedTransaction := events.NewClosure(func(txHash aingle.Hash) {
		txHashesLock.Lock()
		defer txHashesLock.Unlock()

		if _, exists := txHashes[string(txHash)]; exists {
			// tx of bundle was processed
			wgBundleProcessed.Done()
			delete(txHashes, string(txHash))
		}
	})

	var onSolidMilestoneIndexChanged *events.Closure
	if isMilestone {
		onSolidMilestoneIndexChanged = events.NewClosure(func(msIndex milestone.Index) {
			wgBundleProcessed.Done()
		})
	}

	tangleplugin.Events.ProcessedTransaction.Attach(onProcessedTransaction)
	defer tangleplugin.Events.ProcessedTransaction.Detach(onProcessedTransaction)
	if isMilestone {
		tangleplugin.Events.SolidMilestoneIndexChanged.Attach(onSolidMilestoneIndexChanged)
		defer tangleplugin.Events.SolidMilestoneIndexChanged.Detach(onSolidMilestoneIndexChanged)
	}

	for _, t := range b {
		tx := t // assign to new variable, otherwise it would be overwritten by the loop before processed
		txTrits, _ := transaction.TransactionToTrits(tx)
		if err := gossip.Processor().CompressAndEmit(tx, txTrits); err != nil {
			return err
		}
	}

	// wait until all txs of the bundle are processed in the storage layer
	// if it was a milestone, also wait until the solid milestone changed
	wgBundleProcessed.Wait()

	return nil
}

// isBelowMaxDepth checks the below max depth criteria for the given tail transaction.
func isBelowMaxDepth(cachedTailTxMeta *tangle.CachedMetadata) bool {
	defer cachedTailTxMeta.Release(true)

	lsmi := tangle.GetSolidMilestoneIndex()

	_, ortsi := dag.GetTransactionRootSnapshotIndexes(cachedTailTxMeta.Retain(), lsmi) // meta +1

	// if the OTRSI to LSMI delta is over belowMaxDepth, then the tip is invalid.
	return (lsmi - ortsi) > belowMaxDepth
}

// GetEvents returns the events of the coordinator
func GetEvents() *coordinator.CoordinatorEvents {
	if coo == nil {
		return nil
	}
	return coo.Events
}

func configureEvents() {
	// pass all new solid bundles to the selector
	onBundleSolid = events.NewClosure(func(cachedBundle *tangle.CachedBundle) {
		cachedBundle.ConsumeBundle(func(bndl *tangle.Bundle) { // bundle -1

			if bndl.IsInvalidPastCone() || !bndl.IsValid() || !bndl.ValidStrictSemantics() {
				// ignore invalid bundles or semantically invalid bundles or bundles with invalid past cone
				return
			}

			if isBelowMaxDepth(bndl.GetTailMetadata()) {
				// ignore tips that are below max depth
				return
			}

			// add tips to the heaviest branch selector
			if trackedTailsCount := selector.OnNewSolidBundle(bndl); trackedTailsCount >= maxTrackedTails {
				log.Debugf("Coordinator Tipselector: trackedTailsCount: %d", trackedTailsCount)

				// issue next checkpoint
				select {
				case nextCheckpointSignal <- struct{}{}:
				default:
					// do not block if already another signal is waiting
				}
			}
		})
	})

	onMilestoneConfirmed = events.NewClosure(func(confirmation *whiteflag.Confirmation) {
		ts := time.Now()

		// do not propagate during syncing, because it is not needed at all
		if !tangle.IsNodeSyncedWithThreshold() {
			return
		}

		// propagate new transaction root snapshot indexes to the future cone for URTS
		dag.UpdateTransactionRootSnapshotIndexes(confirmation.Mutations.TailsReferenced, confirmation.MilestoneIndex)

		log.Debugf("UpdateTransactionRootSnapshotIndexes finished, took: %v", time.Since(ts).Truncate(time.Millisecond))
	})

	onIssuedCheckpointTransaction = events.NewClosure(func(checkpointIndex int, tipIndex int, tipsTotal int, txHash aingle.Hash) {
		log.Infof("checkpoint (%d) transaction issued (%d/%d): %v", checkpointIndex+1, tipIndex+1, tipsTotal, txHash.Trytes())
	})

	onIssuedMilestone = events.NewClosure(func(index milestone.Index, tailTxHash aingle.Hash) {
		log.Infof("milestone issued (%d): %v", index, tailTxHash.Trytes())
	})
}

func attachEvents() {
	tangleplugin.Events.BundleSolid.Attach(onBundleSolid)
	tangleplugin.Events.MilestoneConfirmed.Attach(onMilestoneConfirmed)
	coo.Events.IssuedCheckpointTransaction.Attach(onIssuedCheckpointTransaction)
	coo.Events.IssuedMilestone.Attach(onIssuedMilestone)
}

func detachEvents() {
	tangleplugin.Events.BundleSolid.Detach(onBundleSolid)
	tangleplugin.Events.MilestoneConfirmed.Detach(onMilestoneConfirmed)
	coo.Events.IssuedMilestone.Detach(onIssuedMilestone)
}
