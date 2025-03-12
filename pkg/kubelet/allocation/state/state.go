/*
Copyright 2021 The Kubernetes Authors.

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

package state

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

type PodResourceInfo struct {
	ContainerResources map[string]v1.ResourceRequirements
	PodLevelResources  v1.ResourceRequirements
}

// PodResourceInfoMap type is used in tracking resources allocated to pod's containers
type PodResourceInfoMap map[types.UID]PodResourceInfo

// Clone returns a copy of PodResourceInfoMap
func (pr PodResourceInfoMap) Clone() PodResourceInfoMap {
	prCopy := make(PodResourceInfoMap)
	for podUID, podInfo := range pr {
		prCopy[podUID] = PodResourceInfo{
			PodLevelResources:  *podInfo.PodLevelResources.DeepCopy(),
			ContainerResources: make(map[string]v1.ResourceRequirements),
		}
		for containerName, containerInfo := range podInfo.ContainerResources {
			prCopy[podUID].ContainerResources[containerName] = *containerInfo.DeepCopy()
		}
	}
	return prCopy
}

// Reader interface used to read current pod resource allocation state
type Reader interface {
	GetContainerResources(podUID types.UID, containerName string) (v1.ResourceRequirements, bool)
	GetPodLevelResources(podUID types.UID) v1.ResourceRequirements
	GetPodResourceInfoMap() PodResourceInfoMap
}

type writer interface {
	SetContainerResources(podUID types.UID, containerName string, alloc v1.ResourceRequirements) error
	SetPodLevelResources(podUID types.UID, alloc v1.ResourceRequirements) error
	SetPodResourceInfoMap(podUID types.UID, alloc PodResourceInfo) error
	Delete(podUID types.UID, containerName string) error
	// RemoveOrphanedPods removes the stored state for any pods not included in the set of remaining pods.
	RemoveOrphanedPods(remainingPods sets.Set[types.UID])
}

// State interface provides methods for tracking and setting pod resource allocation
type State interface {
	Reader
	writer
}
