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

package allocation

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	resourcehelper "k8s.io/component-helpers/resource"
	"k8s.io/klog/v2"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	v1qos "k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/cm"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
	"k8s.io/kubernetes/pkg/kubelet/lifecycle"
	"k8s.io/kubernetes/pkg/kubelet/metrics"
	kubetypes "k8s.io/kubernetes/pkg/kubelet/types"
	"k8s.io/kubernetes/pkg/kubelet/util/format"
)

// NewPodResizesAdmitHandler returns a PodAdmitHandler which is used to evaluate
// if a pod resize can be allocated by the kubelet.
func NewPodResizesAdmitHandler(containerManager cm.ContainerManager, resourceManager cm.ResourceManager, containerRuntime kubecontainer.Runtime, allocationManager Manager) lifecycle.PodAdmitHandler {
	return &podResizesAdmitHandler{
		containerManager:  containerManager,
		resourceManager:   resourceManager,
		containerRuntime:  containerRuntime,
		allocationManager: allocationManager,
	}
}

type podResizesAdmitHandler struct {
	containerManager  cm.ContainerManager
	resourceManager   cm.ResourceManager
	containerRuntime  kubecontainer.Runtime
	allocationManager Manager
}

func (h *podResizesAdmitHandler) Admit(ctx context.Context, attrs *lifecycle.PodAdmitAttributes) lifecycle.PodAdmitResult {
	if attrs.Operation != lifecycle.ResizeOperation {
		return lifecycle.PodAdmitResult{Admit: true}
	}

	logger := klog.FromContext(ctx)
	pod := attrs.Pod
	allocatedPod, _ := h.allocationManager.UpdatePodFromAllocation(pod)
	if resizable, msg, reason := IsInPlacePodVerticalScalingAllowed(pod); !resizable {
		// If there is a pending resize but the resize is not allowed, always use the allocated resources.
		metrics.PodInfeasibleResizes.WithLabelValues(reason).Inc()
		return lifecycle.PodAdmitResult{Admit: false, Reason: v1.PodReasonInfeasible, Message: msg}
	}
	if resizeNotAllowed, msg := disallowResizeForSwappableContainers(h.containerRuntime, pod, allocatedPod); resizeNotAllowed {
		// If this resize involve swap recalculation, set as infeasible, as IPPR with swap is not supported for beta.
		metrics.PodInfeasibleResizes.WithLabelValues("swap_limitation").Inc()
		return lifecycle.PodAdmitResult{Admit: false, Reason: v1.PodReasonInfeasible, Message: msg}
	}
	if !apiequality.Semantic.DeepEqual(pod.Spec.Resources, allocatedPod.Spec.Resources) {
		if resizable, msg, reason := IsInPlacePodLevelResourcesVerticalScalingAllowed(pod); !resizable {
			// If there is a pending pod-level resources resize but the resize is not allowed, always use the allocated resources.
			metrics.PodInfeasibleResizes.WithLabelValues(reason).Inc()
			return lifecycle.PodAdmitResult{Admit: false, Reason: v1.PodReasonInfeasible, Message: msg}
		}
	}

	if disallowed, msg, reason := disallowResizeForMemoryBackedVolumes(pod, allocatedPod, h.containerManager.GetNodeConfig().CgroupVersion); disallowed {
		logger.V(3).Info(msg, "pod", format.Pod(pod))
		metrics.PodInfeasibleResizes.WithLabelValues(reason).Inc()
		return lifecycle.PodAdmitResult{Admit: false, Reason: v1.PodReasonInfeasible, Message: msg}
	}

	if v1qos.GetPodQOS(pod) == v1.PodQOSGuaranteed {
		if !utilfeature.DefaultFeatureGate.Enabled(features.InPlacePodVerticalScalingExclusiveCPUs) {
			cpuMode := h.resourceManager.AllocationMode(v1.ResourceCPU)
			if cpuMode == cm.ResourceAllocationModeExclusive && h.guaranteedPodResourceResizeRequired(pod, v1.ResourceCPU) {
				msg := fmt.Sprintf("Resize is infeasible for Guaranteed Pods when CPU allocation mode is %s", cpuMode)
				logger.V(3).Info(msg, "pod", format.Pod(pod))
				metrics.PodInfeasibleResizes.WithLabelValues("guaranteed_pod_cpu_manager_static_policy").Inc()
				return lifecycle.PodAdmitResult{Admit: false, Reason: v1.PodReasonInfeasible, Message: msg}
			}
		}
		if !utilfeature.DefaultFeatureGate.Enabled(features.InPlacePodVerticalScalingExclusiveMemory) {
			memMode := h.resourceManager.AllocationMode(v1.ResourceMemory)
			if memMode == cm.ResourceAllocationModeExclusive && h.guaranteedPodResourceResizeRequired(pod, v1.ResourceMemory) {
				msg := fmt.Sprintf("Resize is infeasible for Guaranteed Pods when memory allocation mode is %s", memMode)
				logger.V(3).Info(msg, "pod", format.Pod(pod))
				metrics.PodInfeasibleResizes.WithLabelValues("guaranteed_pod_memory_manager_static_policy").Inc()
				return lifecycle.PodAdmitResult{Admit: false, Reason: v1.PodReasonInfeasible, Message: msg}
			}
		}
	}

	return lifecycle.PodAdmitResult{Admit: true}
}

func disallowResizeForMemoryBackedVolumes(desiredPod, allocatedPod *v1.Pod, cgroupVersion int) (bool, string, string) {
	if desiredPod == nil || allocatedPod == nil {
		return false, "", ""
	}

	if IsMemoryBackedVolumeResizeRequested(allocatedPod, desiredPod) {
		if !utilfeature.DefaultFeatureGate.Enabled(features.InPlacePodVerticalScalingMemoryBackedVolumes) {
			return true, "Memory-backed emptyDir volume resize is disabled", "memory_volume_resize_gate_off"
		}
		if cgroupVersion == 1 {
			return true, "Memory-backed emptyDir volume resize is not supported on cgroups v1", "cgroups_v1_unsupported"
		}
	}

	return false, "", ""
}

// isRuntimeSwapEnabledForPod reports whether LimitedSwap is active on the runtime.
// kubecontainer.Runtime only exposes per-container swap behavior, which returns NoSwap even on
// a LimitedSwap node when all containers in pod opted out (e.g. req == lim), so probe with a
// swap-eligible Burstable spec using each container's name (preserving FakeRuntime name lookups).
func isRuntimeSwapEnabledForPod(runtime kubecontainer.Runtime, pod *v1.Pod) bool {
	if runtime == nil || pod == nil {
		return false
	}
	probeMemReq := v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI)}
	for container := range podutil.ContainerIter(&pod.Spec, podutil.InitContainers|podutil.Containers) {
		probePod := &v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{{
					Name:      container.Name,
					Resources: v1.ResourceRequirements{Requests: probeMemReq},
				}},
			},
		}
		if runtime.GetContainerSwapBehavior(probePod, &probePod.Spec.Containers[0]) != kubetypes.NoSwap {
			return true
		}
	}
	return false
}

func podMemoryRequest(pod *v1.Pod) resource.Quantity {
	reqs := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{
		ExcludeOverhead:       true,
		SkipPodLevelResources: !utilfeature.DefaultFeatureGate.Enabled(features.PodLevelResources),
	})
	return reqs[v1.ResourceMemory]
}

func podLevelSwapRequestChanged(runtime kubecontainer.Runtime, desiredPod, allocatedPod *v1.Pod) bool {
	if desiredPod == nil || allocatedPod == nil {
		return false
	}
	desiredPLR := resourcehelper.IsPodLevelResourcesSet(desiredPod)
	allocatedPLR := resourcehelper.IsPodLevelResourcesSet(allocatedPod)
	if !desiredPLR && !allocatedPLR {
		return false
	}
	desiredEligible, _ := cm.IsPodEligibleForSwap(desiredPod)
	allocatedEligible, _ := cm.IsPodEligibleForSwap(allocatedPod)
	if !desiredEligible && !allocatedEligible {
		return false
	}
	// Adding or removing pod-level resources changes whether the pod cgroup owns memory.swap.max.
	// When pod-level resources remain set on both specs, compare both the effective pod memory
	// request (which sizes pod memory.swap.max) and explicit pod.spec.resources.requests.memory
	// (which gates unbounded container swap even when aggregated container requests match it).
	if desiredPLR && allocatedPLR &&
		podMemoryRequest(desiredPod).Equal(podMemoryRequest(allocatedPod)) &&
		desiredPod.Spec.Resources.Requests.Memory().Equal(*allocatedPod.Spec.Resources.Requests.Memory()) {
		return false
	}
	return isRuntimeSwapEnabledForPod(runtime, desiredPod)
}

func disallowResizeForSwappableContainers(runtime kubecontainer.Runtime, desiredPod, allocatedPod *v1.Pod) (bool, string) {
	if runtime == nil || desiredPod == nil || allocatedPod == nil {
		return false, ""
	}
	// Pod-level memory request changes cannot recalculate pod cgroup memory.swap.max in place,
	// even if individual containers opt out of swap or have RestartContainer resize policies.
	// Skip when InPlacePodLevelResourcesVerticalScaling is disabled so Admit reports plr_feature_gate_off.
	if utilfeature.DefaultFeatureGate.Enabled(features.PodLevelResources) &&
		utilfeature.DefaultFeatureGate.Enabled(features.InPlacePodLevelResourcesVerticalScaling) &&
		podLevelSwapRequestChanged(runtime, desiredPod, allocatedPod) {
		return true, "In-place resize of pod-level memory requests with swap is not supported."
	}

	restartableMemoryResizePolicy := func(resizePolicies []v1.ContainerResizePolicy) bool {
		for _, policy := range resizePolicies {
			if policy.ResourceName == v1.ResourceMemory {
				return policy.RestartPolicy == v1.RestartContainer
			}
		}
		return false
	}
	allocatedContainers := make(map[string]*v1.Container)
	for container := range podutil.ContainerIter(&allocatedPod.Spec, podutil.InitContainers|podutil.Containers) {
		allocatedContainers[container.Name] = container
	}
	for desiredContainer := range podutil.ContainerIter(&desiredPod.Spec, podutil.InitContainers|podutil.Containers) {
		allocatedContainer, ok := allocatedContainers[desiredContainer.Name]
		if !ok {
			continue
		}
		origMemRequest := desiredContainer.Resources.Requests[v1.ResourceMemory]
		newMemRequest := allocatedContainer.Resources.Requests[v1.ResourceMemory]
		if !origMemRequest.Equal(newMemRequest) && !restartableMemoryResizePolicy(allocatedContainer.ResizePolicy) {
			aSwapBehavior := runtime.GetContainerSwapBehavior(desiredPod, desiredContainer)
			bSwapBehavior := runtime.GetContainerSwapBehavior(allocatedPod, allocatedContainer)
			if aSwapBehavior != kubetypes.NoSwap || bSwapBehavior != kubetypes.NoSwap {
				return true, "In-place resize of containers with swap is not supported."
			}
		}
	}
	return false, ""
}

func (h *podResizesAdmitHandler) guaranteedPodResourceResizeRequired(pod *v1.Pod, resourceName v1.ResourceName) bool {
	for container, containerType := range podutil.ContainerIter(&pod.Spec, podutil.InitContainers|podutil.Containers) {
		if !IsResizableContainer(container, containerType) {
			continue
		}
		requestedResources := container.Resources
		allocatedresources, _ := h.allocationManager.GetContainerResourceAllocation(pod.UID, container.Name)
		// For Guaranteed pods, requests must equal limits, so checking requests is sufficient.
		if !requestedResources.Requests[resourceName].Equal(allocatedresources.Requests[resourceName]) {
			return true
		}
	}
	return false
}

// IsMemoryBackedVolumeResizeRequested returns true if any of the pod's memory-backed emptyDir volumes has its size limit changed.
func IsMemoryBackedVolumeResizeRequested(desiredPod, allocatedPod *v1.Pod) bool {
	if len(allocatedPod.Spec.Volumes) != len(desiredPod.Spec.Volumes) {
		// Validation should prevent this from ever happening, but if it does, assume no resize.
		return false
	}
	for i, allocatedVol := range allocatedPod.Spec.Volumes {
		if !VolHasMemoryBackedEmptyDirSizeLimit(&allocatedVol) {
			continue
		}
		desiredVol := desiredPod.Spec.Volumes[i]
		if !apiequality.Semantic.DeepEqual(allocatedVol.EmptyDir.SizeLimit, desiredVol.EmptyDir.SizeLimit) {
			return true
		}
	}
	return false
}
