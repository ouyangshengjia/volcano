package allocate

import (
	"fmt"
	"math"

	"k8s.io/klog/v2"
	"volcano.sh/apis/pkg/apis/scheduling"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/util"
)

type allocateContext struct {
	queues              *util.PriorityQueue                 // queue of *api.QueueInfo
	jobsByQueue         map[api.QueueID]*util.PriorityQueue // queue of *api.JobInfo
	jobWorksheet        map[api.JobID]*JobWorksheet
	tasksNoHardTopology map[api.JobID]*util.PriorityQueue // queue of *api.TaskInfo, job without any hard network topology policy use this queue
}

type JobWorksheet struct {
	podBunches         *util.PriorityQueue // queue of *api.PodBunchInfo
	podBunchWorksheets map[api.BunchID]*PodBunchWorksheet
}

func (w *JobWorksheet) ShallowCopyFrom(another *JobWorksheet) {
	if another == nil {
		return
	}
	w.podBunches = another.podBunches
	w.podBunchWorksheets = another.podBunchWorksheets
}

func (w *JobWorksheet) Empty() bool {
	return w.podBunches == nil || w.podBunches.Empty()
}

func (w *JobWorksheet) Clone() *JobWorksheet {
	podBunchWorksheets := make(map[api.BunchID]*PodBunchWorksheet)
	for bunchID, tasks := range w.podBunchWorksheets {
		podBunchWorksheets[bunchID] = tasks.Clone()
	}
	return &JobWorksheet{
		podBunches:         w.podBunches.Clone(),
		podBunchWorksheets: podBunchWorksheets,
	}
}

type PodBunchWorksheet struct {
	tasks *util.PriorityQueue // queue of *api.TaskInfo
}

func (w *PodBunchWorksheet) ShallowCopyFrom(another *PodBunchWorksheet) {
	if another == nil {
		return
	}
	w.tasks = another.tasks
}

func (w *PodBunchWorksheet) Empty() bool {
	return w.tasks == nil || w.tasks.Empty()
}

func (w *PodBunchWorksheet) Clone() *PodBunchWorksheet {
	return &PodBunchWorksheet{
		tasks: w.tasks.Clone(),
	}
}

func (alloc *Action) buildAllocateContext() *allocateContext {
	ssn := alloc.session

	actx := &allocateContext{
		queues:              util.NewPriorityQueue(ssn.QueueOrderFn), // queues sort queues by QueueOrderFn.
		jobsByQueue:         make(map[api.QueueID]*util.PriorityQueue),
		jobWorksheet:        make(map[api.JobID]*JobWorksheet),
		tasksNoHardTopology: make(map[api.JobID]*util.PriorityQueue),
	}

	for _, job := range ssn.Jobs {
		// If not config enqueue action, change Pending pg into Inqueue state to avoid blocking job scheduling.
		if job.IsPending() {
			if conf.EnabledActionMap["enqueue"] {
				klog.V(4).Infof("Job <%s/%s> Queue <%s> skip allocate, reason: job status is pending.",
					job.Namespace, job.Name, job.Queue)
				continue
			} else {
				klog.V(4).Infof("Job <%s/%s> Queue <%s> status update from pending to inqueue, reason: no enqueue action is configured.",
					job.Namespace, job.Name, job.Queue)
				job.PodGroup.Status.Phase = scheduling.PodGroupInqueue
			}
		}

		if vr := ssn.JobValid(job); vr != nil && !vr.Pass {
			klog.V(4).Infof("Job <%s/%s> Queue <%s> skip allocate, reason: %v, message %v", job.Namespace, job.Name, job.Queue, vr.Reason, vr.Message)
			continue
		}

		if _, found := ssn.Queues[job.Queue]; !found {
			klog.Warningf("Skip adding Job <%s/%s> because its queue %s is not found",
				job.Namespace, job.Name, job.Queue)
			continue
		}

		worksheet := alloc.organizeJobWorksheet(job)
		if worksheet.Empty() {
			continue
		}

		if _, found := actx.jobsByQueue[job.Queue]; !found {
			actx.jobsByQueue[job.Queue] = util.NewPriorityQueue(ssn.JobOrderFn)
			actx.queues.Push(ssn.Queues[job.Queue])
		}

		klog.V(4).Infof("Added Job <%s/%s> into Queue <%s>", job.Namespace, job.Name, job.Queue)
		actx.jobsByQueue[job.Queue].Push(job)
		actx.jobWorksheet[job.UID] = worksheet

		// job without any hard network topology policy use actx.tasksNoHardTopology
		if !job.ContainsHardTopology() {
			if podbunchWorksheet, exist := worksheet.podBunchWorksheets[job.DefaultPodBunchID()]; exist {
				actx.tasksNoHardTopology[job.UID] = podbunchWorksheet.tasks
			}
		}
	}

	return actx
}

func (alloc *Action) organizeJobWorksheet(job *api.JobInfo) *JobWorksheet {
	ssn := alloc.session

	jWorksheet := &JobWorksheet{
		podBunches:         util.NewPriorityQueue(ssn.PodBunchOrderFn),
		podBunchWorksheets: make(map[api.BunchID]*PodBunchWorksheet),
	}

	for bunchID, podBunch := range job.PodBunches {
		pbWorksheet := &PodBunchWorksheet{
			tasks: util.NewPriorityQueue(ssn.TaskOrderFn),
		}

		for _, task := range podBunch.TaskStatusIndex[api.Pending] {
			// Skip tasks whose pod are scheduling gated
			if task.SchGated {
				continue
			}

			// Skip BestEffort task in 'allocate' action.
			if task.Resreq.IsEmpty() {
				klog.V(4).Infof("Task <%v/%v> is BestEffort task, skip it.",
					task.Namespace, task.Name)
				continue
			}
			pbWorksheet.tasks.Push(task)
		}

		if !pbWorksheet.Empty() {
			jWorksheet.podBunches.Push(podBunch)
			jWorksheet.podBunchWorksheets[bunchID] = pbWorksheet
		}
	}

	return jWorksheet
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

		jobs, found := actx.jobsByQueue[queue.UID]
		if !found || jobs.Empty() {
			klog.V(4).Infof("Can not find jobs for queue %s.", queue.Name)
			continue
		}

		job := jobs.Pop().(*api.JobInfo)

		if job.ContainsHardTopology() {
			jobWorksheet := actx.jobWorksheet[job.UID]

			klog.V(3).InfoS("Try to allocate resource for job contains hard topology", "queue", queue.Name, "job", job.UID,
				"allocatedHyperNode", job.AllocatedHyperNode, "podBunchNum", jobWorksheet.podBunches.Len())
			stmt := alloc.allocateForJob(job, jobWorksheet, ssn.HyperNodes[framework.ClusterTopHyperNode])
			if stmt != nil && ssn.JobReady(job) { // do not commit stmt when job is pipelined
				stmt.Commit()
				alloc.decision.UpdateDecisionToJob(ssn, job, ssn.HyperNodes)

				// There are still left tasks that need to be allocated when min available < replicas, put the job back
				if !jobWorksheet.Empty() {
					jobs.Push(job)
				}
			}
		} else {
			podBunch, pbExist := job.PodBunches[job.DefaultPodBunchID()]
			tasks, tasksExist := actx.tasksNoHardTopology[job.UID]
			if pbExist && tasksExist {
				klog.V(3).InfoS("Try to allocate resource", "queue", queue.Name, "job", job.UID, "taskNum", tasks.Len())
				stmt, _ := alloc.allocateResourcesForTasks(podBunch, tasks, framework.ClusterTopHyperNode)
				// There are still left tasks that need to be allocated when min available < replicas, put the job back
				if tasks.Len() > 0 {
					jobs.Push(job)
				}
				if stmt != nil && ssn.JobReady(job) { // do not commit stmt when job is pipelined
					stmt.Commit()
				}
			} else {
				klog.ErrorS(nil, "Can not find default podBunch or tasks for job", "job", job.UID,
					"podBunchExist", pbExist, "tasksExist", tasksExist)
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
		stmtBackup := make(map[string]*framework.Statement)    // backup the statement after the job is allocated to a hyperNode
		jobWorksheetsBackup := make(map[string]*JobWorksheet)  // backup the job worksheet after the job is allocated to a hyperNode
		podBunchesAllocationScores := make(map[string]float64) // save the podBunches allocation score of the job allocated to a hyperNode

		for _, hyperNode := range hyperNodes {
			var stmtList []*framework.Statement
			var podBunchesAllocationScore float64

			// Clone jobWorksheet and rest job's fit err to make sure it's a clean cache when everytime filter a hyperNode and do not affect each other between hyperNodes.
			job.ResetFitErr()
			jobWorksheetCopy := jobWorksheet.Clone()
			klog.V(3).InfoS("Try to allocate resource for job in hyperNode", "job", job.UID, "hyperNode", hyperNode.Name)

			for !jobWorksheetCopy.podBunches.Empty() {
				podBunch := jobWorksheetCopy.podBunches.Pop().(*api.PodBunchInfo)
				bunchWorksheet := jobWorksheetCopy.podBunchWorksheets[podBunch.UID]

				klog.V(3).InfoS("Try to allocate resource for podBunch", "job", podBunch.Job,
					"podBunch", podBunch.UID, "allocatedHyperNode", podBunch.AllocatedHyperNode, "taskNum", bunchWorksheet.tasks.Len())
				stmt, allocationScore := alloc.allocateForPodBunch(podBunch, bunchWorksheet, hyperNode)

				if stmt != nil && len(stmt.Operations()) > 0 {
					stmtList = append(stmtList, stmt)
					podBunchesAllocationScore += allocationScore
					// push back when podBunch is ready and remain pending task
					if !bunchWorksheet.Empty() {
						jobWorksheetCopy.podBunches.Push(podBunch)
					}
				}

				if ssn.JobReady(job) {
					break
				}
			}

			mergedStmt := framework.SaveOperations(stmtList...)
			if len(mergedStmt.Operations()) == 0 {
				continue // skip recording this empty solution
			}
			if ssn.JobReady(job) || ssn.JobPipelined(job) {
				stmtBackup[hyperNode.Name] = mergedStmt                                // backup successful solution
				jobWorksheetsBackup[hyperNode.Name] = jobWorksheetCopy                 // backup remains podBunches
				podBunchesAllocationScores[hyperNode.Name] = podBunchesAllocationScore // save the podBunches allocation score of the job
			}

			// dry run in every hyperNode
			for _, stmt := range stmtList {
				stmt.Discard()
			}
		}

		if len(podBunchesAllocationScores) == 0 {
			klog.V(5).InfoS("Find solution for job fail", "job", job.UID, "gradient", gradient)
			continue // try next gradient
		}

		bestHyperNode, err := alloc.selectBestHyperNodeForJob(podBunchesAllocationScores, job)
		if err != nil {
			klog.ErrorS(err, "Cannot find best hyper node for job", "job", job.UID, "gradient", gradient)
			return nil
		}

		// recover the stmt
		bestStmt := stmtBackup[bestHyperNode]
		finalStmt := framework.NewStatement(ssn)
		if err = finalStmt.RecoverOperations(bestStmt); err != nil {
			klog.ErrorS(err, "Failed to recover operations", "job", job.UID, "hyperNode", bestHyperNode)
			return nil
		}

		// inherit the remains worksheet after allocate to the best hyperNode
		jobWorksheet.ShallowCopyFrom(jobWorksheetsBackup[bestHyperNode])

		alloc.decision.SaveJobDecision(job.UID, bestHyperNode)
		klog.V(3).InfoS("Allocate job to hyperNode success", "job", job.UID, "hyperNode", bestHyperNode)

		return finalStmt
	}

	klog.V(5).InfoS("Cannot find any solution for job", "job", job.UID)
	return nil
}

func (alloc *Action) allocateForPodBunch(podBunch *api.PodBunchInfo, podBunchWorksheet *PodBunchWorksheet, hyperNodeForJob *api.HyperNodeInfo) (*framework.Statement, float64) {
	ssn := alloc.session
	job := ssn.Jobs[podBunch.Job]

	if podBunchWorksheet == nil || podBunchWorksheet.Empty() {
		klog.V(4).InfoS("Empty podBunch worksheet", "job", podBunch.Job, "podBunch", podBunch.UID)
		return nil, 0
	}

	hyperNodeGradients := ssn.HyperNodeGradientForPodBunchFn(podBunch, hyperNodeForJob)
	for gradient, hyperNodes := range hyperNodeGradients {
		stmtBackup := make(map[string]*framework.Statement)             // backup the statement after the podBunch is allocated to a hyperNode
		podBunchWorksheetsBackup := make(map[string]*PodBunchWorksheet) // backup the podBunch worksheet after the podBunch is allocated to a hyperNode
		totalNodeScores := make(map[string]float64)                     // save the total node score when the podBunch is allocated to a hyperNode

		for _, hyperNode := range hyperNodes {
			// Clone podBunchWorksheet and rest podBunch's fit err to make sure it's a clean cache when everytime filter a hyperNode and do not affect each other between hyperNodes.
			job.ResetPodBunchFitErr(podBunch)
			podBunchWorksheetCopy := podBunchWorksheet.Clone()

			klog.V(3).InfoS("Try to allocate resource for tasks in podBunch", "job", podBunch.Job,
				"podBunch", podBunch.UID, "taskNum", podBunchWorksheetCopy.tasks.Len(), "hyperNode", hyperNode.Name)
			stmt, totalNodeScore := alloc.allocateResourcesForTasks(podBunch, podBunchWorksheetCopy.tasks, hyperNode.Name)

			if stmt != nil && len(stmt.Operations()) > 0 {
				stmtBackup[hyperNode.Name] = framework.SaveOperations(stmt)      // backup successful solution
				podBunchWorksheetsBackup[hyperNode.Name] = podBunchWorksheetCopy // backup remains tasks
				totalNodeScores[hyperNode.Name] = totalNodeScore                 // save the total node score of current hyperNode
				stmt.Discard()                                                   // dry run in every hyperNode
			}
		}

		if len(totalNodeScores) == 0 {
			klog.V(5).InfoS("Find solution for podBunch fail", "podBunch", podBunch.UID, "gradient", gradient)
			continue // try next gradient
		}

		bestHyperNode, bestScore, err := alloc.selectBestHyperNodeForPodBunch(totalNodeScores, podBunch)
		if err != nil {
			klog.ErrorS(err, "Cannot find best hyper node for podBunch", "podBunch", podBunch.UID, "gradient", gradient)
			return nil, 0
		}

		// recover the stmt
		bestStmt := stmtBackup[bestHyperNode]
		finalStmt := framework.NewStatement(ssn)
		if err = finalStmt.RecoverOperations(bestStmt); err != nil {
			klog.ErrorS(err, "Failed to recover operations", "podBunch", podBunch.UID, "hyperNode", bestHyperNode)
			return nil, 0
		}

		// inherit the remains worksheet after allocate to the best hyperNode
		podBunchWorksheet.ShallowCopyFrom(podBunchWorksheetsBackup[bestHyperNode])

		alloc.decision.SavePodBunchDecision(podBunch.Job, hyperNodeForJob.Name, podBunch.UID, bestHyperNode)
		klog.V(3).InfoS("Allocate podBunch to hyperNode success", "podBunch", podBunch.UID, "hyperNode", bestHyperNode, "score", bestScore)

		return finalStmt, bestScore
	}

	klog.V(5).InfoS("Cannot find any solution for podBunch", "podBunch", podBunch.UID)
	return nil, 0
}

// selectBestHyperNodeForJob return the best hyperNode for the job,
// it will score and select the best hyperNode among all available hyperNodes.
func (alloc *Action) selectBestHyperNodeForJob(podBunchesAllocationScores map[string]float64, job *api.JobInfo) (string, error) {
	highestScore := math.Inf(-1)
	bestHyperNode := ""
	for hyperNode, score := range podBunchesAllocationScores {
		if score > highestScore {
			highestScore = score
			bestHyperNode = hyperNode
		}
	}

	if bestHyperNode == "" {
		return "", fmt.Errorf("no solution found for job %s", job.UID)
	}

	return bestHyperNode, nil
}

// selectBestHyperNodeForPodBunch return the best hyperNode for the podBunch,
// it will score and select the best hyperNode among all available hyperNodes.
func (alloc *Action) selectBestHyperNodeForPodBunch(totalNodeScores map[string]float64, podBunch *api.PodBunchInfo) (string, float64, error) {
	if len(totalNodeScores) <= 0 {
		return "", 0, fmt.Errorf("no solution found for podBunch %s", podBunch.UID)
	}

	ssn := alloc.session
	candidateHyperNodeGroups := make(map[string][]*api.NodeInfo)
	for hyperNode := range totalNodeScores {
		candidateHyperNodeGroups[hyperNode] = ssn.RealNodesList[hyperNode]
	}

	hyperNodeScores, err := util.PrioritizeHyperNodes(candidateHyperNodeGroups, totalNodeScores, podBunch, ssn.HyperNodeOrderMapFn)
	if err != nil {
		return "", 0, fmt.Errorf("prioritize hyperNodes for podBunch %s fail: %w", podBunch.UID, err)
	}

	bestHyperNode, bestScore := util.SelectBestHyperNodeAndScore(hyperNodeScores)
	if bestHyperNode == "" {
		return "", 0, fmt.Errorf("cannot find best hyperNode for podBunch %s", podBunch.UID)
	}
	return bestHyperNode, bestScore, nil
}
