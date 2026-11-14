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

package kubelet

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/ktesting"
	"k8s.io/ktesting/initoption"
	"k8s.io/kubernetes/pkg/features"
	cm "k8s.io/kubernetes/pkg/kubelet/cm"
	cmtesting "k8s.io/kubernetes/pkg/kubelet/cm/testing"
)

func TestConvertToAPIPodLevelResourcesStatus(t *testing.T) {
	featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, featuregatetesting.FeatureOverrides{
		features.PodLevelResources:                       true,
		features.InPlacePodLevelResourcesVerticalScaling: true,
	})

	testCases := []struct {
		name             string
		allocatedCPU     string
		readbackShares   uint64
		expectedMilliCPU int64
	}{
		{
			// 50m -> shares=51 -> weight=2 -> shares(readback)=28 -> 28m.
			// Round-trip match: preserve allocated 50m.
			name:             "preserve allocated request for lossy v2 roundtrip readback",
			allocatedCPU:     "50m",
			readbackShares:   28,
			expectedMilliCPU: 50,
		},
		{
			// Readback shares do not match the round-tripped allocated shares:
			// treat as a real actuation and report the readback milliCPU.
			name:             "use actuated request when readback is not a v2 roundtrip",
			allocatedCPU:     "50m",
			readbackShares:   102, // SharesToMilliCPU(102) == 100
			expectedMilliCPU: 100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger, _ := ktesting.NewTestContext(t)

			pod := &v1.Pod{
				Spec: v1.PodSpec{
					Resources: &v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse(tc.allocatedCPU),
							v1.ResourceMemory: resource.MustParse("50Mi"),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("100m"),
							v1.ResourceMemory: resource.MustParse("100Mi"),
						},
					},
					Containers: []v1.Container{{Name: "pause"}},
				},
				Status: v1.PodStatus{Phase: v1.PodRunning},
			}

			cpuPeriod := uint64(100000)
			cpuQuota := int64(0)
			readbackShares := tc.readbackShares
			cpuCfg := &cm.ResourceConfig{
				CPUShares: &readbackShares,
				CPUPeriod: &cpuPeriod,
				CPUQuota:  &cpuQuota,
			}

			mockPCM := cmtesting.NewMockPodContainerManager(t)
			mockPCM.EXPECT().GetPodCgroupConfig(pod, v1.ResourceMemory).Return(nil, nil)
			mockPCM.EXPECT().GetPodCgroupConfig(pod, v1.ResourceCPU).Return(cpuCfg, nil)

			mockCM := cmtesting.NewMockContainerManager(t)
			mockCM.EXPECT().NewPodContainerManager().Return(mockPCM)

			kl := &Kubelet{containerManager: mockCM}
			got := kl.convertToAPIPodLevelResourcesStatus(logger, pod, v1.PodStatus{}, false)
			require.NotNil(t, got)
			require.Equal(t, tc.expectedMilliCPU, got.Requests.Cpu().MilliValue())
		})
	}
}

func TestConvertToAPIPodLevelResourcesStatusMissingCgroup(t *testing.T) {
	featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, featuregatetesting.FeatureOverrides{
		features.PodLevelResources:                       true,
		features.InPlacePodLevelResourcesVerticalScaling: true,
	})

	resources := func(reqCPU, reqMem, limCPU, limMem string) *v1.ResourceRequirements {
		return &v1.ResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse(reqCPU),
				v1.ResourceMemory: resource.MustParse(reqMem),
			},
			Limits: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse(limCPU),
				v1.ResourceMemory: resource.MustParse(limMem),
			},
		}
	}
	podLevelPod := &v1.Pod{
		Spec: v1.PodSpec{
			Resources:  resources("50m", "50Mi", "100m", "100Mi"),
			Containers: []v1.Container{{Name: "pause"}},
		},
		Status: v1.PodStatus{Phase: v1.PodRunning},
	}
	containerOnlyPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{Name: "c1", Resources: *resources("50m", "50Mi", "100m", "100Mi")},
				{Name: "c2", Resources: *resources("25m", "25Mi", "50m", "50Mi")},
			},
		},
		Status: v1.PodStatus{Phase: v1.PodRunning},
	}
	// Last values read from the cgroup while the pod was running.
	runningOldStatus := v1.PodStatus{
		Phase:     v1.PodRunning,
		Resources: resources("75m", "50Mi", "150m", "150Mi"),
	}
	enoent := fmt.Errorf("failed to read memory.max for cgroup /kubepods/podx: %w",
		&os.PathError{Op: "openat2", Path: "/sys/fs/cgroup/kubepods/podx/memory.max", Err: os.ErrNotExist})

	testCases := []struct {
		name          string
		pod           *v1.Pod
		oldStatus     v1.PodStatus
		podIsTerminal bool
		// readErr is returned by both cgroup reads; nil means no read is expected.
		readErr     error
		expected    *v1.ResourceRequirements
		expectError bool
	}{
		{
			name:          "terminal pod reports allocated resources without reading cgroups",
			pod:           podLevelPod,
			oldStatus:     runningOldStatus,
			podIsTerminal: true,
			expected:      resources("50m", "50Mi", "100m", "100Mi"),
		},
		{
			name:          "terminal container-only pod reports aggregated container resources",
			pod:           containerOnlyPod,
			oldStatus:     runningOldStatus,
			podIsTerminal: true,
			expected:      resources("75m", "75Mi", "150m", "150Mi"),
		},
		{
			name:      "wrapped ENOENT on a running pod is not logged as an error",
			pod:       podLevelPod,
			oldStatus: runningOldStatus,
			readErr:   enoent,
			expected:  resources("75m", "50Mi", "150m", "150Mi"),
		},
		{
			name:        "other read errors are logged as errors",
			pod:         podLevelPod,
			readErr:     errors.New("permission denied"),
			expected:    resources("50m", "50Mi", "100m", "100Mi"),
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tCtx := ktesting.Init(t, initoption.BufferLogs(true))
			logger := tCtx.Logger()

			mockCM := cmtesting.NewMockContainerManager(t)
			if tc.readErr != nil {
				mockPCM := cmtesting.NewMockPodContainerManager(t)
				mockPCM.EXPECT().GetPodCgroupConfig(tc.pod, v1.ResourceMemory).Return(nil, tc.readErr)
				mockPCM.EXPECT().GetPodCgroupConfig(tc.pod, v1.ResourceCPU).Return(nil, tc.readErr)
				mockCM.EXPECT().NewPodContainerManager().Return(mockPCM)
			}

			kl := &Kubelet{containerManager: mockCM}
			got := kl.convertToAPIPodLevelResourcesStatus(logger, tc.pod, tc.oldStatus, tc.podIsTerminal)
			require.Truef(t, apiequality.Semantic.DeepEqual(tc.expected, got), "expected %v, got %v", tc.expected, got)

			underlier, ok := logger.GetSink().(ktesting.Underlier)
			require.True(t, ok)
			logs := underlier.GetBuffer().String()
			if tc.expectError {
				require.Contains(t, logs, "ERROR failed to read memory cgroup config for the pod")
				require.Contains(t, logs, "ERROR failed to read cpu cgroup config for the pod")
			} else {
				require.NotContains(t, logs, "ERROR")
			}
		})
	}
}
