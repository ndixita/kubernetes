//go:build linux

/*
Copyright 2025 The Kubernetes Authors.

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
	v1 "k8s.io/api/core/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-helpers/resource"
	"k8s.io/kubernetes/pkg/features"
)

func isPodLevelResourcesSupported(pod *v1.Pod) PodAdmitResult {
	return isPodLevelResourcesSupportedForAttrs(&PodAdmitAttributes{Pod: pod, Operation: AddOperation})
}

func isPodLevelResourcesSupportedForAttrs(attrs *PodAdmitAttributes) PodAdmitResult {
	pod := attrs.Pod
	podLevelResourcesEnabled := utilfeature.DefaultFeatureGate.Enabled(features.PodLevelResources)
	allPodLevelOpts := resource.PodResourcesOptions{
		PodLevelResourcesEphemeralStorageEnabled: true,
	}
	if resource.IsPodLevelResourcesSetWithOpts(pod, allPodLevelOpts) && !podLevelResourcesEnabled {
		return PodAdmitResult{
			Admit:   false,
			Reason:  PodLevelResourcesNotAdmittedReason,
			Message: "PodLevelResources feature gate is disabled",
		}
	}
	// Reject new pods that specify pod-level ephemeral-storage when the feature gate is
	// disabled. Already-admitted pods (including Pending pods with started init containers,
	// checkpointed static pods re-evaluated on kubelet restart, or in-place CPU/memory
	// resizes after rolling back PodLevelResourcesEphemeralStorage) continue running and
	// fall back to container-level ephemeral-storage accounting.
	if pod.Spec.Resources != nil && !utilfeature.DefaultFeatureGate.Enabled(features.PodLevelResourcesEphemeralStorage) &&
		!isPodAlreadyAdmitted(attrs) {
		_, hasReq := pod.Spec.Resources.Requests[v1.ResourceEphemeralStorage]
		_, hasLim := pod.Spec.Resources.Limits[v1.ResourceEphemeralStorage]
		if hasReq || hasLim {
			return PodAdmitResult{
				Admit:   false,
				Reason:  PodLevelResourcesNotAdmittedReason,
				Message: "PodLevelResourcesEphemeralStorage feature gate is disabled",
			}
		}
	}
	return PodAdmitResult{Admit: true}
}
