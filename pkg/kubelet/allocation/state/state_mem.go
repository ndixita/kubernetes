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
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

type stateMemory struct {
	sync.RWMutex
	podInfoMap PodResourceInfoMap
}

var _ State = &stateMemory{}

// NewStateMemory creates new State to track resources allocated to pods
func NewStateMemory(alloc PodResourceInfoMap) State {
	if alloc == nil {
		alloc = PodResourceInfoMap{}
	}
	klog.V(2).InfoS("Initialized new in-memory state store for pod resource allocation tracking")
	return &stateMemory{
		podInfoMap: alloc,
	}
}

func (s *stateMemory) GetContainerResources(podUID types.UID, containerName string) (v1.ResourceRequirements, bool) {
	s.RLock()
	defer s.RUnlock()

	alloc, ok := s.podInfoMap[podUID].ContainerResources[containerName]
	return *alloc.DeepCopy(), ok
}

func (s *stateMemory) GetPodLevelResources(podUId types.UID) v1.ResourceRequirements {
	s.RLock()
	defer s.RUnlock()
	alloc := s.podInfoMap[podUId].PodLevelResources
	return *alloc.DeepCopy()
}

func (s *stateMemory) GetPodResourceInfoMap() PodResourceInfoMap {
	s.RLock()
	defer s.RUnlock()
	return s.podInfoMap.Clone()
}

func (s *stateMemory) SetContainerResources(podUID types.UID, containerName string, alloc v1.ResourceRequirements) error {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podInfoMap[podUID]; !ok {
		s.podInfoMap[podUID] = PodResourceInfo{
			ContainerResources: make(map[string]v1.ResourceRequirements),
		}
	}

	s.podInfoMap[podUID].ContainerResources[containerName] = alloc
	klog.V(3).InfoS("Updated container resource allocation", "podUID", podUID, "containerName", containerName, "alloc", alloc)
	return nil
}

func (s *stateMemory) SetPodLevelResources(podUID types.UID, alloc v1.ResourceRequirements) error {
	s.Lock()
	defer s.Unlock()
	if _, ok := s.podInfoMap[podUID]; !ok {
		s.podInfoMap[podUID] = PodResourceInfo{PodLevelResources: v1.ResourceRequirements{}}
	}

	podInfo := s.podInfoMap[podUID]
	podInfo.PodLevelResources = alloc
	s.podInfoMap[podUID] = podInfo
	klog.V(3).InfoS("Updated pod level resources", "podUID", podUID, "allocation", alloc)
	return nil
}

func (s *stateMemory) SetPodResourceInfoMap(podUID types.UID, alloc PodResourceInfo) error {
	s.Lock()
	defer s.Unlock()

	s.podInfoMap[podUID] = alloc
	klog.V(3).InfoS("Updated pod resource allocation", "podUID", podUID, "allocation", alloc)
	return nil
}

func (s *stateMemory) deleteContainer(podUID types.UID, containerName string) {
	delete(s.podInfoMap[podUID].ContainerResources, containerName)
	if len(s.podInfoMap[podUID].ContainerResources) == 0 {
		delete(s.podInfoMap, podUID)
	}
	klog.V(3).InfoS("Deleted pod resource allocation", "podUID", podUID, "containerName", containerName)
}

func (s *stateMemory) Delete(podUID types.UID, containerName string) error {
	s.Lock()
	defer s.Unlock()
	if len(containerName) == 0 {
		delete(s.podInfoMap, podUID)
		klog.V(3).InfoS("Deleted pod resource allocation and resize state", "podUID", podUID)
		return nil
	}
	s.deleteContainer(podUID, containerName)
	return nil
}

func (s *stateMemory) RemoveOrphanedPods(remainingPods sets.Set[types.UID]) {
	s.Lock()
	defer s.Unlock()

	for podUID := range s.podInfoMap {
		if _, ok := remainingPods[types.UID(podUID)]; !ok {
			delete(s.podInfoMap, podUID)
		}
	}
}
