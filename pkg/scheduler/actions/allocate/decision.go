package allocate

import (
	"k8s.io/klog/v2"

	"volcano.sh/volcano/pkg/scheduler/api"
)

type Decision struct {
	jobDecisions      map[api.JobID]string
	podBunchDecisions map[api.JobID]map[string]map[api.BunchID]string
}

func NewDecision() *Decision {
	return &Decision{
		jobDecisions:      make(map[api.JobID]string),
		podBunchDecisions: make(map[api.JobID]map[string]map[api.BunchID]string),
	}
}

func (d *Decision) SaveJobDecision(job api.JobID, hyperNodeForJob string) {
	d.jobDecisions[job] = hyperNodeForJob
}

func (d *Decision) SavePodBunchDecision(job api.JobID, hyperNodeForJob string, podBunch api.BunchID, hyperNodeForPodBunch string) {
	if d.podBunchDecisions[job] == nil {
		d.podBunchDecisions[job] = make(map[string]map[api.BunchID]string)
	}
	if d.podBunchDecisions[job][hyperNodeForJob] == nil {
		d.podBunchDecisions[job][hyperNodeForJob] = make(map[api.BunchID]string)
	}
	d.podBunchDecisions[job][hyperNodeForJob][podBunch] = hyperNodeForPodBunch
}

func (d *Decision) UpdateDecisionToJob(job *api.JobInfo, hyperNodes api.HyperNodeInfoMap) {
	hyperNodeForJob := d.jobDecisions[job.UID]
	if hyperNodeForJob == "" {
		return
	}

	jobAllocatedHyperNode := hyperNodes.GetLCAHyperNode(job.AllocatedHyperNode, hyperNodeForJob)
	klog.V(3).InfoS("update allocated hyperNode for job", "job", job.UID,
		"old", job.AllocatedHyperNode, "new", jobAllocatedHyperNode)
	job.AllocatedHyperNode = jobAllocatedHyperNode

	for bunchId, hyperNode := range d.podBunchDecisions[job.UID][hyperNodeForJob] {
		podBunch, found := job.PodBunches[bunchId]
		if !found {
			klog.Errorf("podBunch %s not found", bunchId)
			continue
		}
		allocatedHyperNode := hyperNodes.GetLCAHyperNode(podBunch.AllocatedHyperNode, hyperNode)
		klog.V(3).InfoS("update allocated hyperNode for podBunch", "podBunch", podBunch.UID,
			"old", podBunch.AllocatedHyperNode, "new", allocatedHyperNode)
		podBunch.AllocatedHyperNode = allocatedHyperNode
	}
}
