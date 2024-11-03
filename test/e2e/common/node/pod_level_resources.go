package node

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubecm "k8s.io/kubernetes/pkg/kubelet/cm"
	"k8s.io/kubernetes/test/e2e/feature"
	"k8s.io/kubernetes/test/e2e/framework"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	imageutils "k8s.io/kubernetes/test/utils/image"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	Cgroupv2MemLimit  string = "/sys/fs/cgroup/memory.max"
	Cgroupv2CPULimit  string = "/sys/fs/cgroup/cpu.max"
	Cgroupv2CPUWeight string = "/sys/fs/cgroup/cpu.weight"
	CPUPeriod         string = "100000"
)

var _ = SIGDescribe("Pod Level Resources", framework.WithSerial(), feature.PodLevelResources, "[NodeAlphaFeature:PodLevelResources]", func() {
	f := framework.NewDefaultFramework("pod-level-resources-tests")
	f.NamespacePodSecurityLevel = admissionapi.LevelPrivileged

	ginkgo.BeforeEach(func(ctx context.Context) {
		_, err := e2enode.GetRandomReadySchedulableNode(ctx, f.ClientSet)
		framework.ExpectNoError(err)
		if framework.NodeOSDistroIs("windows") {
			e2eskipper.Skipf("not supported")
		}
	})
	podLevelResourcesTests(f)
})

type ContainerInfo struct {
	Name      string
	Resources *ResourceInfo
}
type ResourceInfo struct {
	CPUReq string
	CPULim string
	MemReq string
	MemLim string
}

func makeContainer(info ContainerInfo) v1.Container {
	cmd := []string{"/bin/sh", "-c", "sleep 1d"}
	res := getResourceInfo(info.Resources)
	return v1.Container{
		Name:      info.Name,
		Command:   cmd,
		Resources: res,
		Image:     imageutils.GetE2EImage(imageutils.BusyBox),
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      "sysfscgroup",
				MountPath: "/tmp",
			},
		},
	}
}

func getResourceInfo(info *ResourceInfo) v1.ResourceRequirements {
	var res v1.ResourceRequirements
	if info != nil {
		if info.CPUReq != "" || info.MemReq != "" {
			res.Requests = make(v1.ResourceList)
		}
		if info.CPUReq != "" {
			res.Requests[v1.ResourceCPU] = resource.MustParse(info.CPUReq)
		}
		if info.MemReq != "" {
			res.Requests[v1.ResourceMemory] = resource.MustParse(info.MemReq)
		}

		if info.CPULim != "" || info.MemLim != "" {
			res.Limits = make(v1.ResourceList)
		}
		if info.CPULim != "" {
			res.Limits[v1.ResourceCPU] = resource.MustParse(info.CPULim)
		}
		if info.MemLim != "" {
			res.Limits[v1.ResourceMemory] = resource.MustParse(info.MemLim)
		}
	}
	return res
}

func makePod(metadata *metav1.ObjectMeta, podResources *ResourceInfo, containers []ContainerInfo) *v1.Pod {
	var testContainers []v1.Container
	for _, container := range containers {
		testContainers = append(testContainers, makeContainer(container))
	}

	pod := &v1.Pod{
		ObjectMeta: *metadata,

		Spec: v1.PodSpec{
			Containers: testContainers,
			Volumes: []v1.Volume{
				{
					Name: "sysfscgroup",
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{Path: "/sys/fs/cgroup"},
					},
				},
			},
		},
	}

	if podResources != nil {
		res := getResourceInfo(podResources)
		pod.Spec.Resources = &res
	}

	return pod
}

func VerifyPodResources(gotPod v1.Pod, expectedInfo ResourceInfo) {
	ginkgo.GinkgoHelper()
	expectedResources := getResourceInfo(&expectedInfo)
	gomega.Expect(gotPod.Spec.Resources).To(gomega.Equal(&expectedResources))
}

func VerifyQoS(gotPod v1.Pod, expectedQoS v1.PodQOSClass) {
	ginkgo.GinkgoHelper()
	gomega.Expect(gotPod.Status.QOSClass).To(gomega.Equal(expectedQoS))
}

func VerifyPodCgroups(ctx context.Context, f *framework.Framework, pod *v1.Pod, expectedResources ResourceInfo) error {
	ginkgo.GinkgoHelper()
	if pod.Spec.Resources != nil {
		cmd := fmt.Sprintf("find /tmp -name '*%s*'", strings.ReplaceAll(string(pod.UID), "-", "_"))
		framework.Logf("Namespace %s Pod %s container - finding Pod cgroup directory path: %q", cmd)
		podCgPath, stderr, err := e2epod.ExecCommandInContainerWithFullOutput(f, pod.Name, pod.Spec.Containers[0].Name, []string{"/bin/sh", "-c", cmd}...)
		if err != nil || len(stderr) > 0 {
			return fmt.Errorf("encountered error while running command: %q, \nerr: %v \nstdErr: %q", cmd, err, stderr)
		}

		cpuWeightCgPath := fmt.Sprintf("%s/cpu.weight", podCgPath)
		expectedCPUShares := int64(kubecm.MilliCPUToShares(pod.Spec.Resources.Requests.Cpu().MilliValue()))
		// convert cgroup v1 cpu.shares value to cgroup v2 cpu.weight value
		// https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/2254-cgroup-v2#phase-1-convert-from-cgroups-v1-settings-to-v2
		expectedCPUShares = int64(1 + ((expectedCPUShares-2)*9999)/262142)
		err = e2epod.VerifyCgroupValue(f, pod, pod.Spec.Containers[0].Name, cpuWeightCgPath, strconv.FormatInt(expectedCPUShares, 10))
		if err != nil {
			return fmt.Errorf("failed to verify cpu request cgroup value: %v", err)
		}

		cpuLimCgPath := fmt.Sprintf("%s/cpu.max", podCgPath)
		cpuQuota := kubecm.MilliCPUToQuota(pod.Spec.Resources.Limits.Cpu().MilliValue(), kubecm.QuotaPeriod)
		expectedCPULimit := strconv.FormatInt(cpuQuota, 10)
		expectedCPULimit = fmt.Sprintf("%s %s", expectedCPULimit, CPUPeriod)
		err = e2epod.VerifyCgroupValue(f, pod, pod.Spec.Containers[0].Name, cpuLimCgPath, expectedCPULimit)
		if err != nil {
			return err
		}

		memLimCgPath := fmt.Sprintf("%s/memory.max", podCgPath)
		expectedMemLim := strconv.FormatInt(pod.Spec.Resources.Limits.Memory().Value(), 10)
		err = e2epod.VerifyCgroupValue(f, pod, pod.Spec.Containers[0].Name, memLimCgPath, expectedMemLim)
		if err != nil {
			return err
		}

	}
	return nil
}

func podLevelResourcesTests(f *framework.Framework) {
	type expectedPodConfig struct {
		qos          v1.PodQOSClass
		podResources *ResourceInfo
	}

	type testCase struct {
		name         string
		podResources *ResourceInfo
		containers   []ContainerInfo
		expected     expectedPodConfig
	}

	tests := []testCase{
		{
			name:         "Guaranteed QoS pod, no container resources",
			podResources: &ResourceInfo{CPUReq: "100m", CPULim: "100m", MemReq: "100Mi", MemLim: "100Mi"},
			containers:   []ContainerInfo{{Name: "c1"}, {Name: "c2"}},
			expected: expectedPodConfig{
				qos:          v1.PodQOSGuaranteed,
				podResources: &ResourceInfo{CPUReq: "100m", CPULim: "100m", MemReq: "100Mi", MemLim: "100Mi"},
			},
		},
		{
			name:         "Guaranteed QoS pod with container resources",
			podResources: &ResourceInfo{CPUReq: "100m", CPULim: "100m", MemReq: "100Mi", MemLim: "100Mi"},
			containers: []ContainerInfo{
				{Name: "c1", Resources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"}},
				{Name: "c2", Resources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"}},
			},
			expected: expectedPodConfig{
				qos:          v1.PodQOSGuaranteed,
				podResources: &ResourceInfo{CPUReq: "100m", CPULim: "100m", MemReq: "100Mi", MemLim: "100Mi"},
			},
		},
		{
			name:         "Guaranteed QoS pod, 1 container with resources",
			podResources: &ResourceInfo{CPUReq: "100m", CPULim: "100m", MemReq: "100Mi", MemLim: "100Mi"},
			containers: []ContainerInfo{
				{Name: "c1", Resources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"}},
				{Name: "c2"},
			},
			expected: expectedPodConfig{
				qos:          v1.PodQOSGuaranteed,
				podResources: &ResourceInfo{CPUReq: "100m", CPULim: "100m", MemReq: "100Mi", MemLim: "100Mi"},
			},
		},
		{
			name:         "Burstable QoS pod, no container resources",
			podResources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"},
			containers: []ContainerInfo{
				{Name: "c1"},
				{Name: "c2"},
			},
			expected: expectedPodConfig{
				qos:          v1.PodQOSBurstable,
				podResources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"},
			},
		},
		{
			name:         "Burstable QoS pod with container resources",
			podResources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"},
			containers: []ContainerInfo{
				{Name: "c1", Resources: &ResourceInfo{CPUReq: "20m", CPULim: "100m", MemReq: "20Mi", MemLim: "100Mi"}},
				{Name: "c2", Resources: &ResourceInfo{CPUReq: "30m", CPULim: "100m", MemReq: "30Mi", MemLim: "100Mi"}},
			},
			expected: expectedPodConfig{
				qos:          v1.PodQOSBurstable,
				podResources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"},
			},
		},
		{
			name:         "Burstable QoS pod, 1 container with resources",
			podResources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"},
			containers: []ContainerInfo{
				{Name: "c1", Resources: &ResourceInfo{CPUReq: "20m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"}},
				{Name: "c2"},
			},
			expected: expectedPodConfig{
				qos:          v1.PodQOSBurstable,
				podResources: &ResourceInfo{CPUReq: "50m", CPULim: "100m", MemReq: "50Mi", MemLim: "100Mi"},
			},
		},
	}

	for _, tc := range tests {
		ginkgo.It(tc.name, func(ctx context.Context) {
			podClient := e2epod.NewPodClient(f)
			tStamp := strconv.Itoa(time.Now().Nanosecond())
			podMetadata := &metav1.ObjectMeta{Name: "testpod", Namespace: f.Namespace.Name, Labels: map[string]string{"time": tStamp}}
			testPod := makePod(podMetadata, tc.podResources, tc.containers)

			ginkgo.By("creating pods")
			pod := podClient.CreateSync(ctx, testPod)

			ginkgo.By("verifying pod resources are as expected")
			VerifyPodResources(*pod, *tc.podResources)

			ginkgo.By("verifying pod QoS as expected")
			VerifyQoS(*pod, tc.expected.qos)

			ginkgo.By("verifying pod cgroup values")
			err := VerifyPodCgroups(ctx, f, pod, *tc.podResources)
			framework.ExpectNoError(err, "failed to verify pod's cgroup values: %v", err)

			ginkgo.By("verifying containers cgroup limits are same as pod container's cgroup limits")
			err = VerifyContainersCgroupLimits(f, pod)
			framework.ExpectNoError(err, "failed to verify containers cgroup values: %v", err)

			ginkgo.By("deleting pods")
			delErr := e2epod.DeletePodWithWait(ctx, f.ClientSet, pod)
			framework.ExpectNoError(delErr, "failed to delete pod %s", delErr)
		})
	}
}

func VerifyContainersCgroupLimits(f *framework.Framework, pod *v1.Pod) error {
	var err error
	for _, container := range pod.Spec.Containers {
		if pod.Spec.Resources.Limits.Memory() != nil && container.Resources.Limits.Memory() == nil {
			expectedCgroupMemLimit := strconv.FormatInt(pod.Spec.Resources.Limits.Memory().Value(), 10)
			err = e2epod.VerifyCgroupValue(f, pod, container.Name, Cgroupv2MemLimit, expectedCgroupMemLimit)
			if err != nil {
				return fmt.Errorf("failed to verify memory limit cgroup value: %v", err)
			}
		}

		if pod.Spec.Resources.Limits.Cpu() != nil && container.Resources.Limits.Cpu() == nil {
			cpuQuota := kubecm.MilliCPUToQuota(pod.Spec.Resources.Limits.Cpu().MilliValue(), kubecm.QuotaPeriod)
			expectedCPULimit := strconv.FormatInt(cpuQuota, 10)
			expectedCPULimit = fmt.Sprintf("%s %s", expectedCPULimit, CPUPeriod)
			err = e2epod.VerifyCgroupValue(f, pod, container.Name, Cgroupv2CPULimit, expectedCPULimit)
			if err != nil {
				return fmt.Errorf("failed to verify cpu limit cgroup value: %v", err)
			}
		}
	}
	return err
}
