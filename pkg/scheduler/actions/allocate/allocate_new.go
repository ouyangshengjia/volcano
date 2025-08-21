package allocate

import (
	"k8s.io/klog/v2"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/util"
)

type allocateContext struct {
	queues       *util.PriorityQueue                 // queue of api.QueueInfo
	jobsByQueue  map[api.QueueID]*util.PriorityQueue // queue of api.JobInfo
	jobWorksheet map[api.JobID]*JobWorksheet
}

type JobWorksheet struct {
	podBunches         *util.PriorityQueue
	podBunchWorksheets map[api.BunchID]*PodBunchWorksheet
}

func (w *JobWorksheet) ShallowCopyFrom(another *JobWorksheet) {
	w.podBunches = another.podBunches
	w.podBunchWorksheets = another.podBunchWorksheets
}

func (w *JobWorksheet) Empty() bool {
	return w.podBunches.Empty()
}

func (w *JobWorksheet) Clone() *JobWorksheet {
	tasksByPodBunch := make(map[api.BunchID]*PodBunchWorksheet)
	for bunchID, tasks := range w.podBunchWorksheets {
		tasksByPodBunch[bunchID] = tasks.Clone()
	}
	return &JobWorksheet{
		podBunches:         w.podBunches.Clone(),
		podBunchWorksheets: tasksByPodBunch,
	}
}

type PodBunchWorksheet struct {
	tasks *util.PriorityQueue
}

func (w *PodBunchWorksheet) ShallowCopyFrom(another *PodBunchWorksheet) {
	w.tasks = another.tasks
}

func (w *PodBunchWorksheet) Empty() bool {
	return w.tasks.Empty()
}

func (w *PodBunchWorksheet) Clone() *PodBunchWorksheet {
	return &PodBunchWorksheet{
		tasks: w.tasks.Clone(),
	}
}

func (alloc *Action) allocateResourcesNew(actx *allocateContext) {
	ssn := alloc.session

	queues := actx.queues
	for {
		if queues.Empty() {
			break
		}

		queue := queues.Pop().(*api.QueueInfo)

		if ssn.Overused(queue) {
			klog.V(3).Infof("Queue <%s> is overused, ignore it.", queue.Name)
			continue
		}

		klog.V(3).Infof("Try to allocate resource to Jobs in Queue <%s>", queue.Name)

		jobs, found := actx.jobsByQueue[queue.UID]
		if !found || jobs.Empty() {
			klog.V(4).Infof("Can not find jobs for queue %s.", queue.Name)
			continue
		}

		job := jobs.Pop().(*api.JobInfo)
		jobWorksheet := actx.jobWorksheet[job.UID]

		stmt := alloc.allocateForJob(job, jobWorksheet, ssn.HyperNodes[api.ClusterRootHyperNode])
		if stmt != nil {
			stmt.Commit()

			// There are still left tasks that need to be allocated when min available < replicas, put the job back
			if !jobWorksheet.Empty() {
				jobs.Push(job)
			}
		}

		// Put back the queue to priority queue after job's resource allocating finished,
		// To ensure that the priority of the queue is calculated based on the latest resource allocation situation.
		queues.Push(queue)
	}
}

func (alloc *Action) allocateForJob(job *api.JobInfo, jobWorksheet *JobWorksheet, hyperNodeToAllocate *api.HyperNodeInfo) *framework.Statement {
	ssn := alloc.session

	if jobWorksheet == nil || jobWorksheet.Empty() {
		klog.V(4).InfoS("Empty job worksheet", "job", job.UID)
		return nil
	}

	hyperNodeGradients := ssn.HyperNodeGradientForJobFn(job, hyperNodeToAllocate)
	for gradient, hyperNodes := range hyperNodeGradients {
		stmtBackup := make(map[string]*framework.Statement)   // backup the statement after allocate to hyperNodes
		jobWorksheetsBackup := make(map[string]*JobWorksheet) // backup the job worksheet after allocate to hyperNodes

		for _, hyperNode := range hyperNodes {
			var stmtList []*framework.Statement

			// Clone jobWorksheet and rest job's fit err to make sure it's a clean cache when everytime filter a hyperNode and do not affect each other between hyperNodes.
			job.ResetFitErr()
			jobWorksheetCopy := jobWorksheet.Clone()
			klog.V(3).InfoS("Try to allocate resource for job in hyperNode", "job", job.UID, "hyperNode", hyperNode.Name)

			for !jobWorksheetCopy.podBunches.Empty() {
				podBunch := jobWorksheetCopy.podBunches.Pop().(*api.PodBunchInfo)
				bunchWorksheet := jobWorksheetCopy.podBunchWorksheets[podBunch.UID]

				stmt := alloc.allocateForPodBunch(podBunch, bunchWorksheet, hyperNode)

				if stmt != nil && len(stmt.Operations()) > 0 {
					stmtList = append(stmtList, stmt)
					// push back when podBunch is ready and remain pending task
					if !bunchWorksheet.Empty() {
						jobWorksheetCopy.podBunches.Push(podBunch)
					}
				}

				if jobReady(job) {
					stmtBackup[hyperNode.Name] = framework.MergeOperations(stmtList...) // backup successful solution
					jobWorksheetsBackup[hyperNode.Name] = jobWorksheetCopy              // backup remains podBunches
					break
				}
			}

			// dry run in every hyperNode
			for _, stmt := range stmtList {
				stmt.Discard()
			}
		}

		if len(stmtBackup) == 0 {
			klog.V(5).InfoS("Find solution for job fail", "job", job.UID, "gradient", gradient)
			continue // try next gradient
		}

		klog.V(5).InfoS("Find solution for job success", "job", job.UID, "gradient", gradient)

		bestHyperNode, stmt := alloc.selectBestHyperNodeForJob(job, stmtBackup)
		jobWorksheet.ShallowCopyFrom(jobWorksheetsBackup[bestHyperNode])

		return stmt
	}

	klog.V(5).InfoS("Cannot find any solution for job", "job", job.UID)
	return nil
}

func (alloc *Action) allocateForPodBunch(podBunch *api.PodBunchInfo, podBunchWorksheet *PodBunchWorksheet, hyperNodeToAllocate *api.HyperNodeInfo) *framework.Statement {
	ssn := alloc.session

	if podBunchWorksheet == nil || podBunchWorksheet.Empty() {
		klog.V(4).InfoS("Empty podBunch worksheet", "job", podBunch.Job, "podBunch", podBunch.UID)
		return nil
	}

	hyperNodeGradients := ssn.HyperNodeGradientForPodBunchFn(podBunch, hyperNodeToAllocate)
	for gradient, hyperNodes := range hyperNodeGradients {
		stmtBackup := make(map[string]*framework.Statement)             // backup the statement after allocate to hyperNodes
		podBunchWorksheetsBackup := make(map[string]*PodBunchWorksheet) // backup the podBunch worksheet after allocate to hyperNodes

		for _, hyperNode := range hyperNodes {
			// Clone podBunchWorksheet to make sure it's a clean cache when everytime filter a hyperNode and do not affect each other between hyperNodes.
			podBunchWorksheetCopy := podBunchWorksheet.Clone()
			klog.V(3).InfoS("try to allocate resource for podBunch in hyperNode", "job", podBunch.Job, "podBunch", podBunch.UID, "hyperNode", hyperNode.Name)

			stmt := alloc.allocateForTasksInPodBunch(podBunch, podBunchWorksheetCopy.tasks, ssn.RealNodesList[hyperNode.Name])

			if stmt != nil && len(stmt.Operations()) > 0 {
				stmtBackup[hyperNode.Name] = stmt                                // backup successful solution
				podBunchWorksheetsBackup[hyperNode.Name] = podBunchWorksheetCopy // backup remains tasks
				stmt.Discard()                                                   // dry run in every hyperNode
			}
		}

		if len(stmtBackup) == 0 {
			klog.V(5).InfoS("find solution for podBunch fail", "podBunch", podBunch.UID, "gradient", gradient)
			continue // try next gradient
		}

		klog.V(5).InfoS("find solution for podBunch success", "podBunch", podBunch.UID, "gradient", gradient)

		bestHyperNode, stmt := alloc.selectBestHyperNodeForPodBunch(podBunch, stmtBackup)
		podBunchWorksheet.ShallowCopyFrom(podBunchWorksheetsBackup[bestHyperNode])

		return stmt
	}

	klog.V(5).InfoS("cannot find any solution for podBunch", "podBunch", podBunch.UID)
	return nil
}

func (alloc *Action) allocateForTasksInPodBunch(podBunch *api.PodBunchInfo, tasks *util.PriorityQueue, nodes []*api.NodeInfo) *framework.Statement {
	return nil
}

func (alloc *Action) selectBestHyperNodeForJob(job *api.JobInfo, stmtByHyperNode map[string]*framework.Statement) (string, *framework.Statement) {
	return "", nil
}

func (alloc *Action) selectBestHyperNodeForPodBunch(podBunch *api.PodBunchInfo, stmtByHyperNode map[string]*framework.Statement) (string, *framework.Statement) {
	return "", nil
}

func jobReady(job *api.JobInfo) bool {
	return true
}
