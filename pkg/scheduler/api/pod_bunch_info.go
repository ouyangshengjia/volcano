package api

import "k8s.io/apimachinery/pkg/types"

type BunchID types.UID

type PodBunchInfo struct {
	UID BunchID
	Job JobID

	TaskStatusIndex map[TaskStatus]tasksMap
}
