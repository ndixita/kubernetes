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
	"slices"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-helpers/nodedeclaredfeatures/types"
)

func TestRequirements(t *testing.T) {
	reqs := Feature.Requirements()
	if reqs == nil {
		t.Fatalf("Feature %s returned nil Requirements", Feature.Name())
	}
	wantGates := []string{PodLevelResourcesFeatureGate, PodLevelResourcesEphemeralStorageFeatureGate}
	if !slices.Equal(reqs.EnabledFeatureGates, wantGates) {
		t.Fatalf("Feature %s Requirements = %v, want %v", Feature.Name(), reqs.EnabledFeatureGates, wantGates)
	}
}

func TestDiscover(t *testing.T) {
	tests := []struct {
		name                       string
		plrEnabled                 bool
		plrEphemeralStorageEnabled bool
		expected                   bool
	}{
		{
			name:                       "both gates enabled",
			plrEnabled:                 true,
			plrEphemeralStorageEnabled: true,
			expected:                   true,
		},
		{
			name:                       "PodLevelResourcesEphemeralStorage disabled",
			plrEnabled:                 true,
			plrEphemeralStorageEnabled: false,
			expected:                   false,
		},
		{
			name:                       "PodLevelResources disabled",
			plrEnabled:                 false,
			plrEphemeralStorageEnabled: true,
			expected:                   false,
		},
		{
			name:                       "both gates disabled",
			plrEnabled:                 false,
			plrEphemeralStorageEnabled: false,
			expected:                   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &types.NodeConfiguration{
				FeatureGates: types.FeatureGateMap{
					PodLevelResourcesFeatureGate:                 tc.plrEnabled,
					PodLevelResourcesEphemeralStorageFeatureGate: tc.plrEphemeralStorageEnabled,
				},
			}
			if got := Feature.Discover(config); got != tc.expected {
				t.Fatalf("Discover() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestInferForScheduling(t *testing.T) {
	tests := []struct {
		name     string
		podInfo  *types.PodInfo
		expected bool
	}{
		{
			name:     "nil podInfo",
			podInfo:  nil,
			expected: false,
		},
		{
			name:     "nil spec",
			podInfo:  &types.PodInfo{},
			expected: false,
		},
		{
			name:     "nil resources",
			podInfo:  &types.PodInfo{Spec: &v1.PodSpec{}},
			expected: false,
		},
		{
			name: "pod-level cpu and memory only",
			podInfo: &types.PodInfo{
				Spec: &v1.PodSpec{
					Resources: &v1.ResourceRequirements{
						Requests: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")},
						Limits:   v1.ResourceList{v1.ResourceMemory: resource.MustParse("1Gi")},
					},
				},
			},
			expected: false,
		},
		{
			name: "container-level ephemeral-storage only",
			podInfo: &types.PodInfo{
				Spec: &v1.PodSpec{
					Containers: []v1.Container{{
						Name: "c1",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("2Gi")},
							Limits:   v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("4Gi")},
						},
					}},
				},
			},
			expected: false,
		},
		{
			name: "pod-level ephemeral-storage request",
			podInfo: &types.PodInfo{
				Spec: &v1.PodSpec{
					Resources: &v1.ResourceRequirements{
						Requests: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("2Gi")},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod-level ephemeral-storage limit",
			podInfo: &types.PodInfo{
				Spec: &v1.PodSpec{
					Resources: &v1.ResourceRequirements{
						Limits: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("4Gi")},
					},
				},
			},
			expected: true,
		},
		{
			name: "pending pod with pod-level ephemeral-storage requires feature",
			podInfo: &types.PodInfo{
				Spec: &v1.PodSpec{
					Resources: &v1.ResourceRequirements{
						Limits: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("4Gi")},
					},
				},
				Status: &v1.PodStatus{Phase: v1.PodPending},
			},
			expected: true,
		},
		{
			name: "running status does not alter spec-based InferForScheduling",
			podInfo: &types.PodInfo{
				Spec: &v1.PodSpec{
					Resources: &v1.ResourceRequirements{
						Limits: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("4Gi")},
					},
				},
				Status: &v1.PodStatus{Phase: v1.PodRunning},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Feature.InferForScheduling(tc.podInfo); got != tc.expected {
				t.Fatalf("InferForScheduling() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestInferForUpdate(t *testing.T) {
	podInfo := &types.PodInfo{
		Spec: &v1.PodSpec{
			Resources: &v1.ResourceRequirements{
				Requests: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("2Gi")},
			},
		},
	}
	if Feature.InferForUpdate(nil, podInfo) {
		t.Fatalf("expected InferForUpdate to be false")
	}
}
