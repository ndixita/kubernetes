/*
Copyright The Kubernetes Authors.

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

package podlevelephemeralstorage

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/component-helpers/nodedeclaredfeatures/types"
)

// Ensure the feature struct implements the unified Feature interface.
var _ types.Feature = &podLevelEphemeralStorageFeature{}

const (
	// PodLevelResourcesFeatureGate is the feature gate for PodLevelResources.
	PodLevelResourcesFeatureGate = "PodLevelResources"
	// PodLevelResourcesEphemeralStorageFeatureGate is the feature gate for PodLevelResourcesEphemeralStorage.
	PodLevelResourcesEphemeralStorageFeatureGate = "PodLevelResourcesEphemeralStorage"
)

// Feature is the implementation of the `PodLevelResourcesEphemeralStorage` feature.
var Feature = &podLevelEphemeralStorageFeature{}

type podLevelEphemeralStorageFeature struct{}

func (f *podLevelEphemeralStorageFeature) Name() string {
	return PodLevelResourcesEphemeralStorageFeatureGate
}

func (f *podLevelEphemeralStorageFeature) Discover(cfg *types.NodeConfiguration) bool {
	return cfg.FeatureGates.Enabled(PodLevelResourcesFeatureGate) &&
		cfg.FeatureGates.Enabled(PodLevelResourcesEphemeralStorageFeatureGate)
}

func (f *podLevelEphemeralStorageFeature) Requirements() *types.FeatureRequirements {
	return &types.FeatureRequirements{
		EnabledFeatureGates: []string{
			PodLevelResourcesFeatureGate,
			PodLevelResourcesEphemeralStorageFeatureGate,
		},
	}
}

func (f *podLevelEphemeralStorageFeature) InferForScheduling(podInfo *types.PodInfo) bool {
	if podInfo == nil || podInfo.Spec == nil || podInfo.Spec.Resources == nil {
		return false
	}
	if _, ok := podInfo.Spec.Resources.Requests[v1.ResourceEphemeralStorage]; ok {
		return true
	}
	if _, ok := podInfo.Spec.Resources.Limits[v1.ResourceEphemeralStorage]; ok {
		return true
	}
	return false
}

func (f *podLevelEphemeralStorageFeature) InferForUpdate(oldPodInfo, newPodInfo *types.PodInfo) bool {
	// ephemeral-storage at pod-level is not mutable on update/resize.
	return false
}

func (f *podLevelEphemeralStorageFeature) MaxVersion() *version.Version {
	return nil
}
