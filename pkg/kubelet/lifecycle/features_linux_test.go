//go:build linux

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

package lifecycle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	ndf "k8s.io/component-helpers/nodedeclaredfeatures"
	ndftypes "k8s.io/component-helpers/nodedeclaredfeatures/types"
	"k8s.io/kubernetes/pkg/features"
)

func TestIsPodLevelResourcesSupportedLinux(t *testing.T) {
	podWithStorageReq := &v1.Pod{
		Spec: v1.PodSpec{
			Resources: &v1.ResourceRequirements{
				Requests: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("1Gi")},
			},
		},
	}
	podWithStorageLim := &v1.Pod{
		Spec: v1.PodSpec{
			Resources: &v1.ResourceRequirements{
				Limits: v1.ResourceList{v1.ResourceEphemeralStorage: resource.MustParse("2Gi")},
			},
		},
	}
	podWithCPU := &v1.Pod{
		Spec: v1.PodSpec{
			Resources: &v1.ResourceRequirements{
				Requests: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")},
			},
		},
	}

	tests := []struct {
		name                            string
		pod                             *v1.Pod
		podLevelResourcesEnabled        bool
		podLevelEphemeralStorageEnabled bool
		wantAdmit                       bool
		wantReason                      string
		wantMessage                     string
	}{
		{
			name:                            "reject pod-level ephemeral-storage when PodLevelResources disabled",
			pod:                             podWithStorageReq,
			podLevelResourcesEnabled:        false,
			podLevelEphemeralStorageEnabled: false,
			wantAdmit:                       false,
			wantReason:                      PodLevelResourcesNotAdmittedReason,
			wantMessage:                     "PodLevelResources feature gate is disabled",
		},
		{
			name:                            "reject pod-level ephemeral-storage request when PodLevelResourcesEphemeralStorage disabled",
			pod:                             podWithStorageReq,
			podLevelResourcesEnabled:        true,
			podLevelEphemeralStorageEnabled: false,
			wantAdmit:                       false,
			wantReason:                      PodLevelResourcesNotAdmittedReason,
			wantMessage:                     "PodLevelResourcesEphemeralStorage feature gate is disabled",
		},
		{
			name:                            "reject pod-level ephemeral-storage limit when PodLevelResourcesEphemeralStorage disabled",
			pod:                             podWithStorageLim,
			podLevelResourcesEnabled:        true,
			podLevelEphemeralStorageEnabled: false,
			wantAdmit:                       false,
			wantReason:                      PodLevelResourcesNotAdmittedReason,
			wantMessage:                     "PodLevelResourcesEphemeralStorage feature gate is disabled",
		},
		{
			name:                            "admit pod-level cpu when PodLevelResourcesEphemeralStorage disabled",
			pod:                             podWithCPU,
			podLevelResourcesEnabled:        true,
			podLevelEphemeralStorageEnabled: false,
			wantAdmit:                       true,
		},
		{
			name:                            "admit pod-level ephemeral-storage when both gates enabled",
			pod:                             podWithStorageReq,
			podLevelResourcesEnabled:        true,
			podLevelEphemeralStorageEnabled: true,
			wantAdmit:                       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.podLevelEphemeralStorageEnabled {
				featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, featuregatetesting.FeatureOverrides{
					features.NodeDeclaredFeatures:              true,
					features.PodLevelResources:                 tc.podLevelResourcesEnabled,
					features.PodLevelResourcesEphemeralStorage: true,
				})
			} else {
				featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, featuregatetesting.FeatureOverrides{
					features.PodLevelResources:                 tc.podLevelResourcesEnabled,
					features.PodLevelResourcesEphemeralStorage: false,
				})
			}

			got := isPodLevelResourcesSupported(tc.pod)
			require.Equal(t, tc.wantAdmit, got.Admit)
			if !tc.wantAdmit {
				require.Equal(t, tc.wantReason, got.Reason)
				require.Contains(t, got.Message, tc.wantMessage)
			}
		})
	}

	t.Run("rollback allows already-admitted pods and resize operations when PodLevelResourcesEphemeralStorage disabled", func(t *testing.T) {
		featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, featuregatetesting.FeatureOverrides{
			features.PodLevelResources:                 true,
			features.PodLevelResourcesEphemeralStorage: false,
		})

		fw := ndf.DefaultFramework
		declared := fw.DiscoverNodeFeatures(&ndftypes.NodeConfiguration{
			FeatureGates: featureGateFunc(func(key string) bool {
				return utilfeature.DefaultFeatureGate.Enabled(featuregate.Feature(key))
			}),
		})
		nodeFeatures, err := fw.MapSorted(declared)
		require.NoError(t, err)
		ndfHandler := NewDeclaredFeaturesAdmitHandler(fw, nodeFeatures, versionutil.MustParse("1.38.0"))
		featuresHandler := NewPodFeaturesAdmitHandler()

		runningPod := podWithStorageLim.DeepCopy()
		runningPod.Status.Phase = v1.PodRunning
		require.True(t, featuresHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: runningPod, Operation: AddOperation}).Admit)
		require.True(t, ndfHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: runningPod, Operation: AddOperation}).Admit)

		pendingInitRunningPod := podWithStorageLim.DeepCopy()
		pendingInitRunningPod.Status = v1.PodStatus{
			Phase: v1.PodPending,
			InitContainerStatuses: []v1.ContainerStatus{{
				Name:  "data-fetcher",
				State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
			}},
		}
		require.True(t, featuresHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: pendingInitRunningPod, Operation: AddOperation}).Admit)
		require.True(t, ndfHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: pendingInitRunningPod, Operation: AddOperation}).Admit)

		staticPodEmptyStatus := podWithStorageLim.DeepCopy()
		staticPodEmptyStatus.Status = v1.PodStatus{}
		// Previously checkpointed static pod (AlreadyAllocated: true) remains admitted across restart.
		require.True(t, featuresHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: staticPodEmptyStatus, Operation: AddOperation, AlreadyAllocated: true}).Admit)
		require.True(t, ndfHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: staticPodEmptyStatus, Operation: AddOperation, AlreadyAllocated: true}).Admit)
		// Brand-new static pod (AlreadyAllocated: false, empty status) is rejected when gate is off.
		require.False(t, featuresHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: staticPodEmptyStatus, Operation: AddOperation, AlreadyAllocated: false}).Admit)
		require.False(t, ndfHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: staticPodEmptyStatus, Operation: AddOperation, AlreadyAllocated: false}).Admit)

		require.True(t, featuresHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: podWithStorageLim, Operation: ResizeOperation}).Admit)
		require.True(t, ndfHandler.Admit(context.Background(), &PodAdmitAttributes{Pod: podWithStorageLim, Operation: ResizeOperation}).Admit)
	})
}

type featureGateFunc func(string) bool

func (f featureGateFunc) Enabled(key string) bool { return f(key) }
