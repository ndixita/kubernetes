/*
Copyright 2024 The Kubernetes Authors.

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

package collectors

import (
	"strings"
	"testing"
	"time"

	cadvisorapiv2 "github.com/google/cadvisor/lib/model"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/metrics/testutil"
	cadvisortest "k8s.io/kubernetes/pkg/kubelet/cadvisor/testing"
	"k8s.io/kubernetes/pkg/kubelet/cm"
)

type fakePodContainerManager struct {
	cm.PodContainerManager
	podCgroups map[string]bool
}

func (f *fakePodContainerManager) IsPodCgroup(cgroupPath string) (bool, types.UID) {
	if f.podCgroups[cgroupPath] {
		return true, "fake-uid"
	}
	return false, ""
}

func TestCPUThrottlingCollector(t *testing.T) {
	cpuCFSThrottledSecondsDesc = cpuCFSThrottledSecondsDesc.GetRawDesc()

	mockCadvisor := cadvisortest.NewMockInterface(t)
	mockCadvisor.EXPECT().ContainerInfoV2("/", cadvisorapiv2.RequestOptions{
		IdType:    cadvisorapiv2.TypeName,
		Count:     1,
		Recursive: true,
	}).Return(map[string]cadvisorapiv2.ContainerInfo{
		"/kubepods/pod123": {
			Stats: []*cadvisorapiv2.ContainerStats{
				{
					Timestamp: time.Unix(1000, 0),
					Cpu: &cadvisorapiv2.CpuStats{
						CFS: cadvisorapiv2.CpuCFS{
							ThrottledTime: 2500000000, // 2.5s
						},
					},
				},
			},
		},
		"/kubepods/pod123/container1": {
			Stats: []*cadvisorapiv2.ContainerStats{
				{
					Timestamp: time.Unix(1000, 0),
					Cpu: &cadvisorapiv2.CpuStats{
						CFS: cadvisorapiv2.CpuCFS{
							ThrottledTime: 1200000000, // 1.2s
						},
					},
				},
			},
		},
	}, nil)

	pcm := &fakePodContainerManager{
		podCgroups: map[string]bool{
			"/kubepods/pod123": true,
		},
	}

	collector := NewCPUThrottlingCollector(mockCadvisor, pcm)

	expectedMetrics := `
		# HELP kubelet_cpu_cfs_throttled_seconds_total [ALPHA] Total time in seconds that containers were throttled due to exceeding CPU limits.
		# TYPE kubelet_cpu_cfs_throttled_seconds_total counter
		kubelet_cpu_cfs_throttled_seconds_total{scope="container"} 1.2 1000000
		kubelet_cpu_cfs_throttled_seconds_total{scope="pod"} 2.5 1000000
	`

	if err := testutil.CustomCollectAndCompare(collector, strings.NewReader(expectedMetrics), "kubelet_cpu_cfs_throttled_seconds_total"); err != nil {
		t.Fatal(err)
	}
}
