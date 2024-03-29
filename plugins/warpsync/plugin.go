package warpsync

import (
	"time"

	"github.com/Ariwonto/aingle-alpha/pkg/config"
	"github.com/Ariwonto/aingle-alpha/pkg/model/milestone"
	"github.com/Ariwonto/aingle-alpha/pkg/model/tangle"
	"github.com/Ariwonto/aingle-alpha/pkg/peering/peer"
	"github.com/Ariwonto/aingle-alpha/pkg/protocol/rqueue"
	"github.com/Ariwonto/aingle-alpha/pkg/protocol/sting"
	"github.com/Ariwonto/aingle-alpha/pkg/protocol/warpsync"
	"github.com/Ariwonto/aingle-alpha/pkg/shutdown"
	"github.com/Ariwonto/aingle-alpha/plugins/gossip"
	peeringplugin "github.com/Ariwonto/aingle-alpha/plugins/peering"
	tangleplugin "github.com/Ariwonto/aingle-alpha/plugins/tangle"
	"github.com/iotaledger/hive.go/daemon"
	"github.com/iotaledger/hive.go/events"
	"github.com/iotaledger/hive.go/logger"
	"github.com/iotaledger/hive.go/node"
)

var (
	PLUGIN   = node.NewPlugin("WarpSync", node.Enabled, configure, run)
	log      *logger.Logger
	warpSync *warpsync.WarpSync

	onPeerConnected              *events.Closure
	onSolidMilestoneIndexChanged *events.Closure
	onCheckpointUpdated          *events.Closure
	onTargetUpdated              *events.Closure
	onStart                      *events.Closure
	onDone                       *events.Closure
)

func configure(plugin *node.Plugin) {
	log = logger.NewLogger(plugin.Name)
	warpSync = warpsync.New(config.NodeConfig.GetInt(config.CfgWarpSyncAdvancementRange))

	configureEvents()
}

func run(plugin *node.Plugin) {

	daemon.BackgroundWorker("WarpSync[Events]", func(shutdownSignal <-chan struct{}) {
		attachEvents()
		<-shutdownSignal
		detachEvents()
	}, shutdown.PriorityWarpSync)
}

func configureEvents() {

	onPeerConnected = events.NewClosure(func(p *peer.Peer) {
		if !p.Protocol.Supports(sting.FeatureSet) {
			return
		}

		p.Events.HeartbeatUpdated.Attach(events.NewClosure(func(hb *sting.Heartbeat) {
			warpSync.UpdateCurrent(tangle.GetSolidMilestoneIndex())
			warpSync.UpdateTarget(hb.SolidMilestoneIndex)
		}))
	})

	onSolidMilestoneIndexChanged = events.NewClosure(func(msIndex milestone.Index) { // bundle +1
		warpSync.UpdateCurrent(msIndex)
	})

	onCheckpointUpdated = events.NewClosure(func(nextCheckpoint milestone.Index, oldCheckpoint milestone.Index, advRange int32) {
		log.Infof("Checkpoint updated to milestone %d", nextCheckpoint)
		// prevent any requests in the queue above our next checkpoint
		gossip.RequestQueue().Filter(func(r *rqueue.Request) bool {
			return r.MilestoneIndex <= nextCheckpoint
		})
		requestMissingMilestoneApprovees := gossip.MemoizedRequestMissingMilestoneApprovees()
		gossip.BroadcastMilestoneRequests(int(advRange), requestMissingMilestoneApprovees, oldCheckpoint)
	})

	onTargetUpdated = events.NewClosure(func(newTarget milestone.Index) {
		log.Infof("Target updated to milestone %d", newTarget)
	})

	onStart = events.NewClosure(func(targetMsIndex milestone.Index, nextCheckpoint milestone.Index, advRange int32) {
		log.Infof("Synchronizing to milestone %d", targetMsIndex)
		gossip.RequestQueue().Filter(func(r *rqueue.Request) bool {
			return r.MilestoneIndex <= nextCheckpoint
		})
		requestMissingMilestoneApprovees := gossip.MemoizedRequestMissingMilestoneApprovees()
		msRequested := gossip.BroadcastMilestoneRequests(int(advRange), requestMissingMilestoneApprovees)
		// if the amount of requested milestones doesn't correspond to the range,
		// it means we already had the milestones in the database, which suggests
		// that we should manually kick start the milestone solidifier.
		if msRequested != int(advRange) {
			log.Info("Manually starting solidifier, as some milestones are already in the database")
			tangleplugin.TriggerSolidifier()
		}
	})

	onDone = events.NewClosure(func(deltaSynced int, took time.Duration) {
		log.Infof("Synchronized %d milestones in %v", deltaSynced, took)
		gossip.RequestQueue().Filter(nil)
	})
}

func attachEvents() {
	peeringplugin.Manager().Events.PeerConnected.Attach(onPeerConnected)
	tangleplugin.Events.SolidMilestoneIndexChanged.Attach(onSolidMilestoneIndexChanged)
	warpSync.Events.CheckpointUpdated.Attach(onCheckpointUpdated)
	warpSync.Events.TargetUpdated.Attach(onTargetUpdated)
	warpSync.Events.Start.Attach(onStart)
	warpSync.Events.Done.Attach(onDone)
}

func detachEvents() {
	peeringplugin.Manager().Events.PeerConnected.Detach(onPeerConnected)
	tangleplugin.Events.SolidMilestoneIndexChanged.Detach(onSolidMilestoneIndexChanged)
	warpSync.Events.CheckpointUpdated.Detach(onCheckpointUpdated)
	warpSync.Events.TargetUpdated.Detach(onTargetUpdated)
	warpSync.Events.Start.Detach(onStart)
	warpSync.Events.Done.Detach(onDone)
}
