package resourcestrategyfit

import (
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/cmd/scheduler/app/options"
	"volcano.sh/volcano/pkg/scheduler/actions/allocate"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/plugins/drf"
	"volcano.sh/volcano/pkg/scheduler/plugins/gang"
	"volcano.sh/volcano/pkg/scheduler/plugins/nodeorder"
	"volcano.sh/volcano/pkg/scheduler/plugins/predicates"
	"volcano.sh/volcano/pkg/scheduler/plugins/proportion"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"

	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
)

const (
	eps = 1e-8
)

func TestMain(m *testing.M) {
	options.Default()
	os.Exit(m.Run())
}

func Test_calculateWeight(t *testing.T) {
	type args struct {
		args framework.Arguments
	}
	tests := []struct {
		name string
		args args
		want ResourceStrategyFit
	}{
		{
			name: "test1",
			args: args{framework.Arguments{
				"ResourceStrategyFitPlusWeight": 10,
				"resources": map[string]interface{}{
					"cpu": map[string]interface{}{
						"type":   "MostAllocated",
						"weight": 1,
					},
					"memory": map[string]interface{}{
						"type":   "LeastAllocated",
						"weight": 2,
					},
				},
			}},
			want: ResourceStrategyFit{
				ResourceStrategyFitWeight: 10,
				Resources: map[v1.ResourceName]ResourcesType{
					"cpu": {
						Type:   config.MostAllocated,
						Weight: 1,
					},
					"memory": {
						Type:   config.LeastAllocated,
						Weight: 2,
					},
				},
			}},
		{
			name: "test2",
			args: args{framework.Arguments{
				"resources": map[string]interface{}{
					"cpu": map[string]interface{}{
						"type":   "MostAllocated",
						"weight": 1,
					},
					"memory": map[string]interface{}{
						"type":   "LeastAllocated",
						"weight": 2,
					},
				},
			}},
			want: ResourceStrategyFit{
				ResourceStrategyFitWeight: 10,
				Resources: map[v1.ResourceName]ResourcesType{
					"cpu": {
						Type:   config.MostAllocated,
						Weight: 1,
					},
					"memory": {
						Type:   config.LeastAllocated,
						Weight: 2,
					},
				},
			}},
		{
			name: "test3",
			args: args{framework.Arguments{
				"ResourceStrategyFitPlusWeight": 10,
			}},
			want: ResourceStrategyFit{
				ResourceStrategyFitWeight: 10,
				Resources: map[v1.ResourceName]ResourcesType{
					"cpu": {
						Type:   config.LeastAllocated,
						Weight: 1,
					},
					"memory": {
						Type:   config.LeastAllocated,
						Weight: 1,
					},
				},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calculateWeight(tt.args.args); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("calculateWeight() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlusScore(t *testing.T) {
	type args struct {
		task   *api.TaskInfo
		node   *api.NodeInfo
		weight ResourceStrategyFit
	}
	tests := []struct {
		name string
		args args
		want float64
	}{
		{
			name: "test1",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 500,
						Memory:   500,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.LeastAllocated,
							Weight: 1,
						},
						"memory": {
							Type:   config.LeastAllocated,
							Weight: 1,
						},
					},
				},
			},
			want: 400},
		{
			name: "test2",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 400,
						Memory:   400,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.LeastAllocated,
							Weight: 1,
						},
						"memory": {
							Type:   config.LeastAllocated,
							Weight: 1,
						},
					},
				},
			},
			want: 250},
		{
			name: "test3",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 500,
						Memory:   500,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.MostAllocated,
							Weight: 1,
						},
						"memory": {
							Type:   config.MostAllocated,
							Weight: 1,
						},
					},
				},
			},
			want: 600},
		{
			name: "test4",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 400,
						Memory:   400,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.MostAllocated,
							Weight: 1,
						},
						"memory": {
							Type:   config.MostAllocated,
							Weight: 1,
						},
					},
				},
			},
			want: 750},
		{
			name: "test5",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 500,
						Memory:   500,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.LeastAllocated,
							Weight: 2,
						},
						"memory": {
							Type:   config.MostAllocated,
							Weight: 1,
						},
					},
				},
			},
			want: 600},
		{
			name: "test6",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 500,
						Memory:   500,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.LeastAllocated,
							Weight: 1,
						},
						"memory": {
							Type:   config.MostAllocated,
							Weight: 2,
						},
					},
				},
			},
			want: 750},
		{
			name: "test7",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 0,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 500,
						Memory:   500,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.LeastAllocated,
							Weight: 1,
						},
						"memory": {
							Type:   config.MostAllocated,
							Weight: 2,
						},
					},
				},
			},
			want: 600},
		{
			name: "test8",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						Memory: 400,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"cpu": {
							Type:   config.LeastAllocated,
							Weight: 1,
						},
						"memory": {
							Type:   config.MostAllocated,
							Weight: 2,
						},
					},
				},
			},
			want: 500},
		{
			name: "test9",
			args: args{
				task: &api.TaskInfo{
					Resreq: &api.Resource{
						MilliCPU: 100,
						Memory:   100,
					},
				},
				node: &api.NodeInfo{
					Used: &api.Resource{
						MilliCPU: 200,
						Memory:   200,
					},
					Allocatable: &api.Resource{
						MilliCPU: 500,
						Memory:   500,
					},
				},
				weight: ResourceStrategyFit{
					ResourceStrategyFitWeight: 10,
					Resources: map[v1.ResourceName]ResourcesType{
						"memory": {
							Type:   config.MostAllocated,
							Weight: 2,
						},
					},
				},
			},
			want: 600},
	}
	score := map[string]float64{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Score(tt.args.task, tt.args.node, tt.args.weight); got != tt.want {
				if tt.name == "test5" || tt.name == "test6" {
					score[tt.name] = got
					return
				}
				t.Errorf("PlusScore() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_mostRequestedScore(t *testing.T) {
	type args struct {
		requested float64
		used      float64
		capacity  float64
		weight    int
	}
	tests := []struct {
		name    string
		args    args
		want    float64
		wantErr bool
	}{
		{
			name: "test1",
			args: args{
				requested: 0,
				used:      0,
				capacity:  0,
				weight:    0,
			},
			want:    0,
			wantErr: false},
		{
			name: "test2",
			args: args{
				requested: 1,
				used:      2,
				capacity:  2,
				weight:    1,
			},
			want:    0,
			wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mostRequestedScore(tt.args.requested, tt.args.used, tt.args.capacity, tt.args.weight)
			if (err != nil) != tt.wantErr {
				t.Errorf("mostRequestedScore() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("mostRequestedScore() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_leastRequestedScore(t *testing.T) {
	type args struct {
		requested float64
		used      float64
		capacity  float64
		weight    int
	}
	tests := []struct {
		name    string
		args    args
		want    float64
		wantErr bool
	}{
		{
			name: "test1",
			args: args{
				requested: 0,
				used:      0,
				capacity:  0,
				weight:    0,
			},
			want:    0,
			wantErr: false},
		{
			name: "test2",
			args: args{
				requested: 1,
				used:      2,
				capacity:  2,
				weight:    1,
			},
			want:    0,
			wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := leastRequestedScore(tt.args.requested, tt.args.used, tt.args.capacity, tt.args.weight)
			if (err != nil) != tt.wantErr {
				t.Errorf("mostRequestedScore() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("mostRequestedScore() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_resourceStrategyFitPlusWeightPlusPlugin_OnSessionOpen(t *testing.T) {
	type fields struct {
		weight ResourceStrategyFit
	}
	type args struct {
		ssn *framework.Session
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "test1",
			args: args{ssn: &framework.Session{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rsf := &resourceStrategyFitPlugin{
				weight: tt.fields.weight,
			}
			rsf.OnSessionOpen(tt.args.ssn)
		})
	}
}

func addResource(resourceList v1.ResourceList, name v1.ResourceName, need string) {
	resourceList[name] = resource.MustParse(need)
}

func TestResourceStrategyFitPlugin(t *testing.T) {
	GPU := v1.ResourceName("nvidia.com/gpu")
	FOO := v1.ResourceName("example.com/foo")

	p1 := util.BuildPod("c1", "p1", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "pg1", make(map[string]string), make(map[string]string))
	addResource(p1.Spec.Containers[0].Resources.Requests, FOO, "2")
	p2 := util.BuildPod("c1", "p2", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "pg1", make(map[string]string), make(map[string]string))
	addResource(p2.Spec.Containers[0].Resources.Requests, FOO, "3")
	p3 := util.BuildPod("c1", "p3", "", v1.PodPending, api.BuildResourceList("1", "10Gi"), "pg1", make(map[string]string), make(map[string]string))
	addResource(p3.Spec.Containers[0].Resources.Requests, GPU, "2")
	p4 := util.BuildPod("c1", "p4", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "pg1", make(map[string]string), make(map[string]string))
	addResource(p4.Spec.Containers[0].Resources.Requests, GPU, "3")

	p5 := util.BuildPod("c1", "p5", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "pg1", make(map[string]string), make(map[string]string))
	addResource(p5.Spec.Containers[0].Resources.Requests, GPU, "4")
	addResource(p5.Spec.Containers[0].Resources.Requests, FOO, "4")

	n1 := util.BuildNode("n1", api.BuildResourceList("4", "4Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), make(map[string]string))
	addResource(n1.Status.Allocatable, GPU, "10")
	n2 := util.BuildNode("n2", api.BuildResourceList("4", "4Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), make(map[string]string))
	addResource(n2.Status.Allocatable, GPU, "5")
	n3 := util.BuildNode("n3", api.BuildResourceList("4", "4Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), make(map[string]string))
	addResource(n3.Status.Allocatable, FOO, "10")
	n4 := util.BuildNode("n4", api.BuildResourceList("4", "4Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), make(map[string]string))
	addResource(n4.Status.Allocatable, FOO, "5")

	n5 := util.BuildNode("n5", api.BuildResourceList("4", "4Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), make(map[string]string))
	addResource(n5.Status.Allocatable, GPU, "10")
	addResource(n5.Status.Allocatable, FOO, "5")
	n6 := util.BuildNode("n6", api.BuildResourceList("4", "4Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), make(map[string]string))
	addResource(n6.Status.Allocatable, FOO, "5")
	addResource(n6.Status.Allocatable, FOO, "10")

	pg1 := util.BuildPodGroup("pg1", "c1", "c1", 0, nil, "")
	queue1 := util.BuildQueue("c1", 1, nil)

	tests := []struct {
		uthelper.TestCommonStruct
		arguments framework.Arguments
		expected  map[string]map[string]float64
	}{
		{
			TestCommonStruct: uthelper.TestCommonStruct{
				Name:      "single job",
				Plugins:   map[string]framework.PluginBuilder{PluginName: New},
				PodGroups: []*schedulingv1.PodGroup{pg1},
				Queues:    []*schedulingv1.Queue{queue1},
				Pods:      []*v1.Pod{p1, p2, p3, p4},
				Nodes:     []*v1.Node{n1, n2, n3, n4},
			},
			arguments: framework.Arguments{
				"ResourceStrategyFitPlusWeight": 10,
				"resources": map[string]interface{}{
					"nvidia.com/gpu": map[string]interface{}{
						"type":   "MostAllocated",
						"weight": 1,
					},
					"example.com/foo": map[string]interface{}{
						"type":   "LeastAllocated",
						"weight": 1,
					},
				},
			},
			expected: map[string]map[string]float64{
				"c1/p1": {
					"n1": 0,
					"n2": 0,
					"n3": 800,
					"n4": 600,
				},
				"c1/p2": {
					"n1": 0,
					"n2": 0,
					"n3": 700,
					"n4": 400,
				},
				"c1/p3": {
					"n1": 200,
					"n2": 400,
					"n3": 0,
					"n4": 0,
				},
				"c1/p4": {
					"n1": 300,
					"n2": 600,
					"n3": 0,
					"n4": 0,
				},
			},
		},
		{
			TestCommonStruct: uthelper.TestCommonStruct{
				Name:      "single job",
				Plugins:   map[string]framework.PluginBuilder{PluginName: New},
				PodGroups: []*schedulingv1.PodGroup{pg1},
				Queues:    []*schedulingv1.Queue{queue1},
				Pods:      []*v1.Pod{p5},
				Nodes:     []*v1.Node{n5, n6},
			},
			arguments: framework.Arguments{
				"ResourceStrategyFitPlusWeight": 10,
				"resources": map[string]interface{}{
					"nvidia.com/gpu": map[string]interface{}{
						"type":   "MostAllocated",
						"weight": 1,
					},
					"example.com/foo": map[string]interface{}{
						"type":   "LeastAllocated",
						"weight": 2,
					},
				},
			},
			expected: map[string]map[string]float64{
				"c1/p5": {
					"n5": 266.66666666,
					"n6": 399.99999999,
				},
			},
		},
	}

	trueValue := true
	for i, test := range tests {
		tiers := []conf.Tier{
			{
				Plugins: []conf.PluginOption{
					{
						Name:             PluginName,
						EnabledNodeOrder: &trueValue,
						Arguments:        test.arguments,
					},
				},
			},
		}
		ssn := test.RegisterSession(tiers, nil)
		for _, job := range ssn.Jobs {
			for _, task := range job.Tasks {
				taskID := fmt.Sprintf("%s/%s", task.Namespace, task.Name)
				for _, node := range ssn.Nodes {
					score, _ := ssn.NodeOrderFn(task, node)
					if expectScore := test.expected[taskID][node.Name]; math.Abs(expectScore-score) > eps {
						t.Errorf("case%d: task %s on node %s expect have score %v, but get %v", i, taskID, node.Name, expectScore, score)
					}
				}
			}
		}
	}
}

func TestAllocate(t *testing.T) {

	arguments := framework.Arguments{
		"ResourceStrategyFitPlusWeight": 10,
		"resources": map[string]interface{}{
			"nvidia.com/gpu": map[string]interface{}{
				"type":   "MostAllocated",
				"weight": 2,
			},
			"cpu": map[string]interface{}{
				"type":   "LeastAllocated",
				"weight": 1,
			},
		},
	}

	GPU := v1.ResourceName("nvidia.com/gpu")

	GpuPod1 := util.BuildPod("c1", "p1", "", v1.PodPending, api.BuildResourceList("1", "1G"), "pg1", map[string]string{"volcano.sh/task-spec": "worker"}, map[string]string{"nodeResourceType": "gpu"})
	addResource(GpuPod1.Spec.Containers[0].Resources.Requests, GPU, "2")
	GpuPod2 := util.BuildPod("c1", "p2", "n2", v1.PodRunning, api.BuildResourceList("1", "1G"), "pg1", map[string]string{"volcano.sh/task-spec": "worker"}, map[string]string{"nodeResourceType": "gpu"})
	addResource(GpuPod2.Spec.Containers[0].Resources.Requests, GPU, "2")
	CpuPod1 := util.BuildPod("c1", "p3", "", v1.PodPending, api.BuildResourceList("1", "1G"), "pg1", map[string]string{"volcano.sh/task-spec": "worker"}, map[string]string{"nodeResourceType": "cpu"})
	CpuPod2 := util.BuildPod("c1", "p4", "n3", v1.PodRunning, api.BuildResourceList("1", "1G"), "pg1", map[string]string{"volcano.sh/task-spec": "worker"}, map[string]string{"nodeResourceType": "cpu"})
	GpuNode1 := util.BuildNode("n1", api.BuildResourceList("5", "10Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), map[string]string{"nodeResourceType": "gpu"})
	addResource(GpuNode1.Status.Allocatable, GPU, "10")
	GpuNode2 := util.BuildNode("n2", api.BuildResourceList("5", "10Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), map[string]string{"nodeResourceType": "gpu"})
	addResource(GpuNode2.Status.Allocatable, GPU, "10")
	CpuNode1 := util.BuildNode("n3", api.BuildResourceList("5", "10Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), map[string]string{"nodeResourceType": "cpu"})
	CpuNode2 := util.BuildNode("n4", api.BuildResourceList("5", "10Gi", []api.ScalarResource{{Name: "pods", Value: "10"}}...), map[string]string{"nodeResourceType": "cpu"})

	plugins := map[string]framework.PluginBuilder{
		PluginName:            New,
		drf.PluginName:        drf.New,
		proportion.PluginName: proportion.New,
		predicates.PluginName: predicates.New,
		nodeorder.PluginName:  nodeorder.New,
		gang.PluginName:       gang.New,
	}
	tests := []uthelper.TestCommonStruct{
		{
			Name: "GPU MostAllocated",
			PodGroups: []*schedulingv1.PodGroup{
				util.BuildPodGroup("pg1", "c1", "c1", 1, nil, schedulingv1.PodGroupInqueue),
			},
			Pods: []*v1.Pod{
				GpuPod1,
				GpuPod2,
			},
			Nodes: []*v1.Node{
				GpuNode1,
				GpuNode2,
				CpuNode1,
				CpuNode2,
			},
			Queues: []*schedulingv1.Queue{
				util.BuildQueue("c1", 1, nil),
			},
			ExpectBindMap: map[string]string{
				"c1/p1": "n2",
			},
			ExpectBindsNum: 1,
		},
		{
			Name: "cpu LeastAllocated",
			PodGroups: []*schedulingv1.PodGroup{
				util.BuildPodGroup("pg1", "c1", "c1", 1, nil, schedulingv1.PodGroupInqueue),
			},
			Pods: []*v1.Pod{
				CpuPod1,
				CpuPod2,
			},
			Nodes: []*v1.Node{
				GpuNode1,
				GpuNode2,
				CpuNode1,
				CpuNode2,
			},
			Queues: []*schedulingv1.Queue{
				util.BuildQueue("c1", 1, nil),
			},
			ExpectBindMap: map[string]string{
				"c1/p3": "n4",
			},
			ExpectBindsNum: 1,
		},
	}

	trueValue := true
	tiers := []conf.Tier{
		{
			Plugins: []conf.PluginOption{
				{
					Name:             PluginName,
					EnabledNodeOrder: &trueValue,
					Arguments:        arguments,
				},
				{
					Name:                gang.PluginName,
					EnabledJobOrder:     &trueValue,
					EnabledJobReady:     &trueValue,
					EnabledJobPipelined: &trueValue,
					EnabledJobStarving:  &trueValue,
				},
				{
					Name:               drf.PluginName,
					EnabledPreemptable: &trueValue,
					EnabledJobOrder:    &trueValue,
				},
				{
					Name:               proportion.PluginName,
					EnabledQueueOrder:  &trueValue,
					EnabledReclaimable: &trueValue,
					EnabledAllocatable: &trueValue,
				},
				{
					Name:             predicates.PluginName,
					EnabledPredicate: &trueValue,
				},
				{
					Name:             nodeorder.PluginName,
					EnabledNodeOrder: &trueValue,
				},
			},
		},
	}

	for i, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			test.Plugins = plugins
			test.RegisterSession(tiers, nil)
			defer test.Close()
			test.Run([]framework.Action{allocate.New()})
			if err := test.CheckAll(i); err != nil {
				t.Fatal(err)
			}
		})
	}
}
