/*
Copyright 2025 The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package networktopologyaware

import (
	"fmt"
	"sort"

	"k8s.io/klog/v2"
	"k8s.io/utils/set"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/util"
)

const (
	// PluginName indicates name of volcano scheduler plugin.
	PluginName            = "network-topology-aware"
	BaseScore             = 100.0
	ZeroScore             = 0.0
	NetworkTopologyWeight = "weight"
)

type networkTopologyAwarePlugin struct {
	// Arguments given for the plugin
	pluginArguments framework.Arguments
	weight          int
	*hyperNodesTier
}

type hyperNodesTier struct {
	maxTier int
	minTier int
}

func (h *hyperNodesTier) init(hyperNodesSetByTier []int) {
	if len(hyperNodesSetByTier) == 0 {
		return
	}
	h.minTier = hyperNodesSetByTier[0]
	h.maxTier = hyperNodesSetByTier[len(hyperNodesSetByTier)-1]
}

// New function returns prioritizePlugin object
func New(arguments framework.Arguments) framework.Plugin {
	return &networkTopologyAwarePlugin{
		pluginArguments: arguments,
		hyperNodesTier:  &hyperNodesTier{},
		weight:          calculateWeight(arguments),
	}
}

func (nta *networkTopologyAwarePlugin) Name() string {
	return PluginName
}

func calculateWeight(args framework.Arguments) int {
	/*
	   The arguments of the networktopologyaware plugin can refer to the following configuration:
	   tiers:
	   - plugins:
	     - name: network-topology-aware
	       arguments:
	         weight: 10
	*/
	weight := 1
	args.GetInt(&weight, NetworkTopologyWeight)
	return weight
}

func (nta *networkTopologyAwarePlugin) OnSessionOpen(ssn *framework.Session) {
	klog.V(5).Infof("Enter networkTopologyAwarePlugin plugin ...")
	defer func() {
		klog.V(5).Infof("Leaving networkTopologyAware plugin ...")
	}()
	nta.hyperNodesTier.init(ssn.HyperNodesTiers)
	hyperNodeFn := func(podBunch *api.PodBunchInfo, hyperNodes map[string][]*api.NodeInfo) (map[string]float64, error) {
		hyperNodeScores := make(map[string]float64)

		if podBunch.AllocatedHyperNode == "" {
			return hyperNodeScores, nil
		}
		// The job still has remaining tasks to be scheduled, calculate score based on the tier of LCAHyperNode between the hyperNode and jobAllocatedHyperNode.
		var maxScore float64 = -1
		scoreToHyperNodes := map[float64][]string{}
		for hyperNode := range hyperNodes {
			score := nta.networkTopologyAwareScore(hyperNode, podBunch.AllocatedHyperNode, ssn.HyperNodes)
			score *= float64(nta.weight)
			hyperNodeScores[hyperNode] = score
			if score >= maxScore {
				maxScore = score
				scoreToHyperNodes[maxScore] = append(scoreToHyperNodes[maxScore], hyperNode)
			}
		}
		// Calculate score based on the number of tasks scheduled for the job when max score of hyperNode has more than one.
		if len(scoreToHyperNodes[maxScore]) > 1 {
			candidateHyperNodes := scoreToHyperNodes[maxScore]
			for _, hyperNode := range candidateHyperNodes {
				taskNumScore := nta.scoreWithTaskNum(hyperNode, podBunch.Tasks, ssn.RealNodesList)
				taskNumScore *= float64(nta.weight)
				hyperNodeScores[hyperNode] += taskNumScore
			}
		}

		klog.V(4).Infof("networkTopologyAware hyperNode score is: %v", hyperNodeScores)
		return hyperNodeScores, nil
	}

	nodeFn := func(task *api.TaskInfo, nodes []*api.NodeInfo) (map[string]float64, error) {
		nodeScores := make(map[string]float64)

		podBunch := ssn.Jobs[task.Job].PodBunches[task.PodBunch]

		hardMode, _ := podBunch.IsHardTopologyMode()
		if hardMode {
			return nodeScores, nil
		}

		jobAllocatedHyperNode := task.JobAllocatedHyperNode
		if jobAllocatedHyperNode == "" {
			return nodeScores, nil
		}
		// Calculate score based on LCAHyperNode tier.
		var maxScore float64 = -1
		scoreToNodes := map[float64][]string{}
		for _, node := range nodes {
			hyperNode := util.FindHyperNodeForNode(node.Name, ssn.RealNodesList, ssn.HyperNodesTiers, ssn.HyperNodesSetByTier)
			score := nta.networkTopologyAwareScore(hyperNode, jobAllocatedHyperNode, ssn.HyperNodes)
			score *= float64(nta.weight)
			nodeScores[node.Name] = score
			if score >= maxScore {
				maxScore = score
				scoreToNodes[maxScore] = append(scoreToNodes[maxScore], node.Name)
			}
		}
		// Calculate score based on the number of tasks scheduled for the job when max score of node has more than one.
		if len(scoreToNodes[maxScore]) > 1 {
			candidateNodes := scoreToNodes[maxScore]
			for _, node := range candidateNodes {
				hyperNode := util.FindHyperNodeForNode(node, ssn.RealNodesList, ssn.HyperNodesTiers, ssn.HyperNodesSetByTier)
				taskNumScore := nta.scoreWithTaskNum(hyperNode, podBunch.Tasks, ssn.RealNodesList)
				taskNumScore *= float64(nta.weight)
				nodeScores[node] += taskNumScore
			}
		}

		klog.V(4).Infof("networkTopologyAware node score is: %v", nodeScores)
		return nodeScores, nil
	}

	ssn.AddHyperNodeOrderFn(nta.Name(), hyperNodeFn)
	ssn.AddBatchNodeOrderFn(nta.Name(), nodeFn)

	hyperNodeGradientFn := func(hyperNode *api.HyperNodeInfo, highestAllowedTier int, allocatedHyperNode string) ([][]*api.HyperNodeInfo, error) {
		enqueued := set.New[string]()
		var processQueue []*api.HyperNodeInfo

		searchRoot, err := getSearchRoot(ssn.HyperNodes, hyperNode, highestAllowedTier, allocatedHyperNode)
		if err != nil {
			return nil, fmt.Errorf("getSearchRoot failed: %w", err)
		}

		processQueue = append(processQueue, searchRoot)
		enqueued.Insert(searchRoot.Name)

		eligibleHyperNodes := make(map[int][]*api.HyperNodeInfo)
		for len(processQueue) > 0 {
			// pop one hyperNode from queue
			current := processQueue[0]
			processQueue = processQueue[1:]

			if current.Tier() <= highestAllowedTier {
				eligibleHyperNodes[current.Tier()] = append(eligibleHyperNodes[current.Tier()], current)
			}

			// push children hyperNode into queue
			for child := range current.Children {
				if enqueued.Has(child) {
					continue
				}
				processQueue = append(processQueue, ssn.HyperNodes[child])
				enqueued.Insert(child)
			}
		}

		// organize hyperNode gradients by tiers in ascending order
		var tiers []int
		for tier := range eligibleHyperNodes {
			tiers = append(tiers, tier)
		}
		sort.Ints(tiers)

		var result [][]*api.HyperNodeInfo
		for _, tier := range tiers {
			result = append(result, eligibleHyperNodes[tier])
		}

		return result, nil
	}

	ssn.AddHyperNodeGradientForJobFn(nta.Name(), func(job *api.JobInfo, hyperNode *api.HyperNodeInfo) [][]*api.HyperNodeInfo {
		if hardMode, highestAllowedTier := job.IsHardTopologyMode(); hardMode {
			result, err := hyperNodeGradientFn(hyperNode, highestAllowedTier, job.AllocatedHyperNode)
			if err != nil {
				klog.ErrorS(err, "build hyperNode gradient fail", "job", job.UID, "hyperNode", hyperNode.Name,
					"highestAllowedTier", highestAllowedTier, "allocatedHyperNode", job.AllocatedHyperNode)
				return nil
			}
			return result
		}
		return [][]*api.HyperNodeInfo{{hyperNode}}
	})
	ssn.AddHyperNodeGradientForPodBunchFn(nta.Name(), func(podBunch *api.PodBunchInfo, hyperNode *api.HyperNodeInfo) [][]*api.HyperNodeInfo {
		if hardMode, highestAllowedTier := podBunch.IsHardTopologyMode(); hardMode {
			result, err := hyperNodeGradientFn(hyperNode, highestAllowedTier, podBunch.AllocatedHyperNode)
			if err != nil {
				klog.ErrorS(err, "build hyperNode gradient fail", "podBunch", podBunch.UID, "hyperNode", hyperNode.Name,
					"highestAllowedTier", highestAllowedTier, "allocatedHyperNode", podBunch.AllocatedHyperNode)
				return nil
			}
			return result
		}
		return [][]*api.HyperNodeInfo{{hyperNode}}
	})
}

// getSearchRoot returns the intersection of hyperNodeAvailable and hyperNodeHighestAllowed
func getSearchRoot(hyperNodes api.HyperNodeInfoMap, hyperNodeAvailable *api.HyperNodeInfo, highestAllowedTier int, allocatedHyperNode string) (*api.HyperNodeInfo, error) {
	if allocatedHyperNode == "" {
		return hyperNodeAvailable, nil
	}

	hyperNodeHighestAllowed, err := getHighestAllowedHyperNode(hyperNodes, highestAllowedTier, allocatedHyperNode)
	if err != nil {
		return nil, fmt.Errorf("get highest allowed hyperNode failed: %w", err)
	}

	lca := hyperNodes.GetLCAHyperNode(hyperNodeAvailable.Name, hyperNodeHighestAllowed)
	if lca == hyperNodeHighestAllowed {
		return hyperNodeAvailable, nil
	}
	if lca == hyperNodeAvailable.Name {
		hni, ok := hyperNodes[hyperNodeHighestAllowed]
		if !ok {
			return nil, fmt.Errorf("failed to get highest allowed HyperNode info for %s", hyperNodeHighestAllowed)
		}
		return hni, nil
	}

	return nil, fmt.Errorf("hyperNodeAvailable %s and hyperNodeHighestAllowed %s have no intersection",
		hyperNodeAvailable.Name, hyperNodeHighestAllowed)
}

func getHighestAllowedHyperNode(hyperNodes api.HyperNodeInfoMap, highestAllowedTier int, allocatedHyperNode string) (string, error) {
	var highestAllowedHyperNode string

	ancestors := hyperNodes.GetAncestors(allocatedHyperNode)
	for _, ancestor := range ancestors {
		hni, ok := hyperNodes[ancestor]
		if !ok {
			return "", fmt.Errorf("allocated hyperNode %s ancestor %s not found", allocatedHyperNode, ancestor)
		}
		if hni.Tier() > highestAllowedTier {
			break
		}
		highestAllowedHyperNode = ancestor
	}

	if highestAllowedHyperNode == "" {
		return "", fmt.Errorf("allocated hyperNode %s tier is greater than highest allowed tier %d", allocatedHyperNode, highestAllowedTier)
	}

	return highestAllowedHyperNode, nil
}

func (bp *networkTopologyAwarePlugin) OnSessionClose(ssn *framework.Session) {
}

// networkTopologyAwareScore use the best fit polices during scheduling.

// Goals:
// - The tier of LCAHyperNode of the hyperNode and the job allocatedHyperNode should be as low as possible.
func (nta *networkTopologyAwarePlugin) networkTopologyAwareScore(hyperNodeName, jobAllocatedHyperNode string, hyperNodeMap api.HyperNodeInfoMap) float64 {
	if hyperNodeName == jobAllocatedHyperNode {
		return BaseScore
	}
	LCAHyperNode := hyperNodeMap.GetLCAHyperNode(hyperNodeName, jobAllocatedHyperNode)
	hyperNodeInfo, ok := hyperNodeMap[LCAHyperNode]
	if !ok {
		return ZeroScore
	}
	// Calculate score: (maxTier - LCAhyperNode.tier)/(maxTier - minTier)
	hyperNodeTierScore := BaseScore * nta.scoreHyperNodeWithTier(hyperNodeInfo.Tier())
	return hyperNodeTierScore
}

// Goals:
// - Tasks under a job should be scheduled to one hyperNode as much as possible.
func (nta *networkTopologyAwarePlugin) scoreWithTaskNum(hyperNodeName string, tasks api.TasksMap, realNodesList map[string][]*api.NodeInfo) float64 {
	taskNum := util.FindJobTaskNumOfHyperNode(hyperNodeName, tasks, realNodesList)
	taskNumScore := ZeroScore
	if len(tasks) > 0 {
		// Calculate score: taskNum/allTaskNum
		taskNumScore = BaseScore * scoreHyperNodeWithTaskNum(taskNum, len(tasks))
	}
	return taskNumScore
}

func (nta *networkTopologyAwarePlugin) scoreHyperNodeWithTier(tier int) float64 {
	// Use tier to calculate scores and map the original score to the range between 0 and 1.
	if nta.minTier == nta.maxTier {
		return ZeroScore
	}
	return float64(nta.maxTier-tier) / float64(nta.maxTier-nta.minTier)
}

func scoreHyperNodeWithTaskNum(taskNum int, allTaskNum int) float64 {
	// Calculate task distribution rate as score and map the original score to the range between 0 and 1.
	if allTaskNum == 0 {
		return ZeroScore
	}
	return float64(taskNum) / float64(allTaskNum)
}
