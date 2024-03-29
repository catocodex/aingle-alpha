package gossip

import (
	"github.com/Ariwonto/aingle-alpha/pkg/model/milestone"
	"github.com/Ariwonto/aingle-alpha/pkg/model/tangle"
	"github.com/Ariwonto/aingle-alpha/pkg/peering/peer"
	"github.com/Ariwonto/aingle-alpha/pkg/protocol/helpers"
	"github.com/Ariwonto/aingle-alpha/pkg/protocol/sting"
)

// BroadcastHeartbeat broadcasts a heartbeat message to every connected peer who supports STING.
func BroadcastHeartbeat() {
	snapshotInfo := tangle.GetSnapshotInfo()
	if snapshotInfo == nil {
		return
	}

	connected, synced := manager.ConnectedAndSyncedPeerCount()

	heartbeatMsg, _ := sting.NewHeartbeatMessage(tangle.GetSolidMilestoneIndex(), snapshotInfo.PruningIndex, tangle.GetLatestMilestoneIndex(), connected, synced)
	manager.ForAllConnected(func(p *peer.Peer) bool {
		if !p.Protocol.Supports(sting.FeatureSet) {
			return true
		}
		p.EnqueueForSending(heartbeatMsg)
		return true
	})
}

// BroadcastLatestMilestoneRequest broadcasts a milestone request for the latest milestone every connected peer who supports STING.
func BroadcastLatestMilestoneRequest() {
	manager.ForAllConnected(func(p *peer.Peer) bool {
		if !p.Protocol.Supports(sting.FeatureSet) {
			return true
		}
		helpers.SendLatestMilestoneRequest(p)
		return true
	})
}

// BroadcastMilestoneRequests broadcasts up to N requests for milestones nearest to the current solid milestone index
// to every connected peer who supports STING. Returns the number of milestones requested.
func BroadcastMilestoneRequests(rangeToRequest int, onExistingMilestoneInRange func(index milestone.Index), from ...milestone.Index) int {
	var requested int

	// make sure we only request what we don't have
	startingPoint := tangle.GetSolidMilestoneIndex()
	if len(from) > 0 {
		startingPoint = from[0]
	}
	var msIndexes []milestone.Index
	for i := 1; i <= rangeToRequest; i++ {
		toReq := startingPoint + milestone.Index(i)
		// only request if we do not have the milestone
		if !tangle.ContainsMilestone(toReq) {
			requested++
			msIndexes = append(msIndexes, toReq)
			continue
		}
		if onExistingMilestoneInRange != nil {
			onExistingMilestoneInRange(toReq)
		}
	}

	if len(msIndexes) == 0 {
		return requested
	}

	// send each ms request to a random peer who supports the message
	for _, msIndex := range msIndexes {
		manager.ForAllConnected(func(p *peer.Peer) bool {
			if !p.Protocol.Supports(sting.FeatureSet) {
				return true
			}
			if !p.HasDataFor(msIndex) {
				return true
			}
			helpers.SendMilestoneRequest(p, msIndex)
			return false
		})
	}
	return requested
}
