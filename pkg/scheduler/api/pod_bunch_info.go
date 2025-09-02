package api

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/sets"
	"volcano.sh/apis/pkg/apis/scheduling"
)

type BunchID types.UID

type PodBunchInfo struct {
	UID BunchID
	Job JobID

	Priority   int32 // determined by the highest priority task in the podBunch
	MatchIndex int   // the first label value match to the pods in the podBunch

	Tasks           map[TaskID]*TaskInfo
	TaskStatusIndex map[TaskStatus]TasksMap
	taskPriorities  map[int32]sets.Set[TaskID]

	AllocatedHyperNode string

	networkTopology *scheduling.NetworkTopologySpec
}

func NewPodBunchInfo(uid BunchID, job JobID, policy *scheduling.BunchPolicySpec, matchValues []string) *PodBunchInfo {
	pbi := &PodBunchInfo{
		UID: uid,
		Job: job,
	}
	if policy != nil && policy.NetworkTopology != nil {
		pbi.networkTopology = policy.NetworkTopology.DeepCopy()
	}
	if len(matchValues) > 0 {
		if v, err := strconv.Atoi(matchValues[0]); err == nil {
			pbi.MatchIndex = v
		}
	}
	return pbi
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

func (pbi *PodBunchInfo) addTask(ti *TaskInfo) {
	pbi.Tasks[ti.UID] = ti

	if _, found := pbi.TaskStatusIndex[ti.Status]; !found {
		pbi.TaskStatusIndex[ti.Status] = TasksMap{}
	}
	pbi.TaskStatusIndex[ti.Status][ti.UID] = ti

	if _, found := pbi.taskPriorities[ti.Priority]; !found {
		pbi.taskPriorities[ti.Priority] = sets.New[TaskID]()
	}
	pbi.taskPriorities[ti.Priority].Insert(ti.UID)
	if ti.Priority > pbi.Priority {
		pbi.Priority = ti.Priority
	}
}

func (pbi *PodBunchInfo) deleteTask(ti *TaskInfo) {
	delete(pbi.Tasks, ti.UID)

	if tasks, found := pbi.TaskStatusIndex[ti.Status]; found {
		delete(tasks, ti.UID)
		if len(tasks) == 0 {
			delete(pbi.TaskStatusIndex, ti.Status)
		}
	}

	if tasks, found := pbi.taskPriorities[ti.Priority]; found {
		delete(tasks, ti.UID)
		if len(tasks) == 0 {
			delete(pbi.taskPriorities, ti.Priority)
			if ti.Priority > pbi.Priority {
				pbi.Priority = pbi.getTaskHighestPriority()
			}
		}
	}
}

func (pbi *PodBunchInfo) getTaskHighestPriority() int32 {
	var highestPriority int32
	for priority := range pbi.taskPriorities {
		if priority > highestPriority {
			highestPriority = priority
		}
	}
	return highestPriority
}

func getPodBunchMatchValues(policy scheduling.BunchPolicySpec, pod *v1.Pod) []string {
	if len(policy.MatchPolicy) == 0 || pod.Labels == nil {
		return nil
	}

	values := make([]string, 0, len(policy.MatchPolicy))
	for _, p := range policy.MatchPolicy {
		value, ok := pod.Labels[p.LabelKey]
		if !ok || value == "" {
			return nil
		}
		values = append(values, value)
	}
	return values
}

func getPodBunchId(job JobID, policy string, matchValues []string) BunchID {
	id := strings.Join(matchValues, "-")
	if len(id) > 128 {
		hasher := fnv.New32a()
		_, _ = hasher.Write([]byte(id))
		id = rand.SafeEncodeString(fmt.Sprint(hasher.Sum32())) // todo handle collision
	}
	return BunchID(fmt.Sprintf("%s/%s-%s", job, policy, id))
}

func (pbi *PodBunchInfo) IsReady() bool {
	return pbi.ReadyTaskNum()+pbi.PendingBestEffortTaskNum() >= int32(len(pbi.Tasks)) // todo the length of tasks will change
}

func (pbi *PodBunchInfo) IsPipelined() bool {
	return pbi.WaitingTaskNum()+pbi.ReadyTaskNum()+pbi.PendingBestEffortTaskNum() >= int32(len(pbi.Tasks))
}

// ReadyTaskNum returns the number of tasks that are ready or that is best-effort.
func (pbi *PodBunchInfo) ReadyTaskNum() int32 {
	occupied := 0
	occupied += len(pbi.TaskStatusIndex[Bound])
	occupied += len(pbi.TaskStatusIndex[Binding])
	occupied += len(pbi.TaskStatusIndex[Running])
	occupied += len(pbi.TaskStatusIndex[Allocated])
	occupied += len(pbi.TaskStatusIndex[Succeeded])

	return int32(occupied)
}

func (pbi *PodBunchInfo) PendingBestEffortTaskNum() int32 {
	count := 0
	for _, task := range pbi.TaskStatusIndex[Pending] {
		if task.BestEffort {
			count++
		}
	}
	return int32(count)
}

// WaitingTaskNum returns the number of tasks that are pipelined.
func (pbi *PodBunchInfo) WaitingTaskNum() int32 {
	return int32(len(pbi.TaskStatusIndex[Pipelined]))
}
