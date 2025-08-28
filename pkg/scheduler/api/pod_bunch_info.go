package api

import (
	"k8s.io/apimachinery/pkg/types"
	"volcano.sh/apis/pkg/apis/scheduling"
)

type BunchID types.UID

type PodBunchInfo struct {
	UID BunchID
	Job JobID

	Priority int32 // determined by the highest priority task in the podBunch

	Tasks           map[TaskID]*TaskInfo
	TaskStatusIndex map[TaskStatus]TasksMap

	AllocatedHyperNode string

	networkTopology *scheduling.NetworkTopologySpec
}

// IsHardTopologyMode return whether the podBunch's network topology mode is hard and also return the highest allowed tier
func (pbi *PodBunchInfo) IsHardTopologyMode() (bool, int) {
	if pbi.networkTopology == nil || pbi.networkTopology.HighestTierAllowed == nil {
		return false, 0
	}

	return pbi.networkTopology.Mode == scheduling.HardNetworkTopologyMode, *pbi.networkTopology.HighestTierAllowed
}

// IsSoftTopologyMode returns whether the podBunch has configured network topologies with soft mode.
func (pbi *PodBunchInfo) IsSoftTopologyMode() bool {
	if pbi.networkTopology == nil {
		return false
	}
	return pbi.networkTopology.Mode == scheduling.SoftNetworkTopologyMode
}
