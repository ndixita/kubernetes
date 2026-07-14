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
	"path/filepath"

	cadvisorapiv2 "github.com/google/cadvisor/lib/model"
	"k8s.io/component-base/metrics"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/cadvisor"
	"k8s.io/kubernetes/pkg/kubelet/cm"
)

var (
	cpuCFSThrottledSecondsDesc = metrics.NewDesc(
		"kubelet_cpu_cfs_throttled_seconds_total",
		"Total time in seconds that containers were throttled due to exceeding CPU limits.",
		[]string{"scope"},
		nil,
		metrics.ALPHA,
		"",
	)
)

type cpuThrottlingCollector struct {
	metrics.BaseStableCollector
	cadvisor cadvisor.Interface
	pcm      cm.PodContainerManager
}

// NewCPUThrottlingCollector returns a metrics.StableCollector which exports container CPU throttling metrics
func NewCPUThrottlingCollector(cadvisor cadvisor.Interface, pcm cm.PodContainerManager) metrics.StableCollector {
	return &cpuThrottlingCollector{
		cadvisor: cadvisor,
		pcm:      pcm,
	}
}

// DescribeWithStability implements metrics.StableCollector
func (c *cpuThrottlingCollector) DescribeWithStability(ch chan<- *metrics.Desc) {
	ch <- cpuCFSThrottledSecondsDesc
}

// CollectWithStability implements metrics.StableCollector
func (c *cpuThrottlingCollector) CollectWithStability(ch chan<- metrics.Metric) {
	logger := klog.Background()
	infos, err := c.cadvisor.ContainerInfoV2("/", cadvisorapiv2.RequestOptions{
		IdType:    cadvisorapiv2.TypeName,
		Count:     1,
		Recursive: true,
	})
	if err != nil {
		logger.Error(err, "Failed to get container infos for CPU throttling metrics")
		return
	}

	for cgroupPath, info := range infos {
		if len(info.Stats) == 0 {
			continue
		}
		stats := info.Stats[len(info.Stats)-1]
		if stats.Cpu == nil || stats.Cpu.CFS.ThrottledTime == 0 {
			continue
		}

		throttledSeconds := float64(stats.Cpu.CFS.ThrottledTime) / 1e9

		if isPod, _ := c.pcm.IsPodCgroup(cgroupPath); isPod {
			ch <- metrics.NewLazyMetricWithTimestamp(
				stats.Timestamp,
				metrics.NewLazyConstMetric(cpuCFSThrottledSecondsDesc, metrics.CounterValue, throttledSeconds, "pod"),
			)
		} else {
			parent := filepath.Dir(cgroupPath)
			if isPodParent, _ := c.pcm.IsPodCgroup(parent); isPodParent {
				ch <- metrics.NewLazyMetricWithTimestamp(
					stats.Timestamp,
					metrics.NewLazyConstMetric(cpuCFSThrottledSecondsDesc, metrics.CounterValue, throttledSeconds, "container"),
				)
			}
		}
	}
}
