/*
 * Tencent is pleased to support the open source community by making TKEStack available.
 *
 * Copyright (C) 2012-2019 Tencent. All Rights Reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use
 * this file except in compliance with the License. You may obtain a copy of the
 * License at
 *
 * https://opensource.org/licenses/Apache-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
 * WARRANTIES OF ANY KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations under the License.
 */
package util

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog"
)

const (
	VCoreAnnotation         = "nvidia.com/vcuda-core"
	VMemoryAnnotation       = "nvidia.com/vcuda-memory"
	PredicateTimeAnnotation = "nvidia.com/predicate-time"
	PredicateGPUIndexPrefix = "nvidia.com/predicate-gpu-idx-"
	PredicateNode           = "nvidia.com/predicate-node"
	GPUAssigned             = "nvidia.com/gpu-assigned"

	// TODO 节点gpu manager组件心跳
	NodeAnnotationHeartbeat = "tydic.io/node-gpu-heartbeat"
	// Deprecated TODO 检测gpu是否开启mig
	NodeAnnotationDeviceMig = "tydic.io/mig-device-"
	// TODO 节点gpu设备信息注册
	NodeAnnotationDeviceRegister = "tydic.io/nvidia-device-register"

	// TODO 作用于pod上指定要分配的设备类型 例如：A100
	PodAnnotationUseGpuType = "nvidia.com/use-gputype"
	// TODO 作用于pod上指定不要分配的设备类型 例如：3080
	PodAnnotationUnUseGpuType = "nvidia.com/nouse-gputype"

	// 节点绑定时间
	PodLabelBindTime = "tydic.io/bind-time"
	// 设备绑定阶段
	PodLabelDeviceBindPhase = "tydic.io/bind-phase"

	HundredCore = 100

	// TODO 节点设备类型标签
	GPUModelLabel = "gaia.nvidia.com/gpu-model"
)

type DeviceBindPhase string

const (
	DeviceBindAllocating DeviceBindPhase = "allocating"
	DeviceBindFailed     DeviceBindPhase = "failed"
	DeviceBindSuccess    DeviceBindPhase = "success"
)

// TODO 校验设备名称是否符合条件
func CheckDeviceType(annotations map[string]string, deviceType string) bool {
	deviceType = strings.ToUpper(deviceType)
	if use, ok1 := annotations[PodAnnotationUseGpuType]; ok1 {
		useTypes := strings.Split(use, ",")
		if !ContainsSliceFunc(useTypes, func(useType string) bool {
			return strings.Contains(deviceType, strings.ToUpper(useType))
		}) {
			return false
		}
	}
	if unuse, ok2 := annotations[PodAnnotationUnUseGpuType]; ok2 {
		unuseTypes := strings.Split(unuse, ",")
		if ContainsSliceFunc(unuseTypes, func(unuseType string) bool {
			return strings.Contains(deviceType, strings.ToUpper(unuseType))
		}) {
			return false
		}
	}
	return true
}

func ContainsSliceFunc[S ~[]E, E any](s S, filter func(E) bool) bool {
	for _, e := range s {
		if filter(e) {
			return true
		}
	}
	return false
}

// Deprecated 已启弃用这种方式
func IsMig(index int, node *v1.Node) bool {
	migDeviceKey := fmt.Sprint(NodeAnnotationDeviceMig, index)
	if val, ok := node.Annotations[migDeviceKey]; ok {
		return val == "true"
	}
	return false
}

// IsGPURequiredPod tell if the pod is a GPU request pod
func IsGPURequiredPod(pod *v1.Pod) bool {
	klog.V(4).Infof("Determine if the pod %s needs GPU resource", pod.Name)
	// 通过pod中容器定义的resourceName，获取该pod请求了多少cuda核心和显存
	vcore := GetGPUResourceOfPod(pod, VCoreAnnotation)
	vmemory := GetGPUResourceOfPod(pod, VMemoryAnnotation)

	// 当请求的vcuda 为 0 或者 显存 为0 则 返回 false
	// Check if pod request for GPU resource
	if vcore <= 0 || (vcore < HundredCore && vmemory <= 0) {
		klog.V(4).Infof("Pod %s in namespace %s does not Request for GPU resource",
			pod.Name,
			pod.Namespace)
		return false
	}

	return true
}

// IsGPURequiredContainer tell if the container is a GPU request container
func IsGPURequiredContainer(c *v1.Container) bool {
	klog.V(4).Infof("Determine if the container %s needs GPU resource", c.Name)

	vcore := GetGPUResourceOfContainer(c, VCoreAnnotation)
	vmemory := GetGPUResourceOfContainer(c, VMemoryAnnotation)

	// Check if container request for GPU resource
	if vcore <= 0 || (vcore < HundredCore && vmemory <= 0) {
		klog.V(4).Infof("Container %s does not Request for GPU resource", c.Name)
		return false
	}

	return true
}

func Max(a, b int) int {
	if a >= b {
		return a
	}
	return b
}

// GetGPUResourceOfPod returns the limit size of GPU resource of given pod
func GetGPUResourceOfPod(pod *v1.Pod, resourceName v1.ResourceName) uint {
	var total uint
	containers := pod.Spec.Containers
	for _, container := range containers {
		if val, ok := container.Resources.Limits[resourceName]; ok {
			total += uint(val.Value())
		}
	}
	return total
}

// GetGPUResourceOfPod returns the limit size of GPU resource of given container
func GetGPUResourceOfContainer(container *v1.Container, resourceName v1.ResourceName) uint {
	var count uint
	if val, ok := container.Resources.Limits[resourceName]; ok {
		count = uint(val.Value())
	}
	return count
}

// Is the Node has GPU device
func IsGPUEnabledNode(node *v1.Node) bool {
	return GetCapacityOfNode(node, VCoreAnnotation) > 0
}

// Get the capacity of request resource of the Node
func GetCapacityOfNode(node *v1.Node, resourceName string) int {
	val, ok := node.Status.Capacity[v1.ResourceName(resourceName)]

	if !ok {
		return 0
	}

	return int(val.Value())
}

// GetGPUDeviceCountOfNode returns the number of GPU devices
func GetGPUDeviceCountOfNode(node *v1.Node) int {
	val, ok := node.Status.Capacity[VCoreAnnotation]
	if !ok {
		return 0
	}
	return int(val.Value()) / HundredCore
}

// GetPredicateIdxOfContainer returns the idx number of given container should be run on which
// GPU device
func GetPredicateIdxOfContainer(pod *v1.Pod, containerIndex int) ([]int, error) {
	var ret []int
	predicateIndexes, ok := pod.Annotations[PredicateGPUIndexPrefix+strconv.Itoa(containerIndex)]
	if !ok {
		return ret, fmt.Errorf("predicate index for container %d of pod %s not found",
			containerIndex, pod.UID)
	}
	for _, indexStr := range strings.Split(predicateIndexes, ",") {
		index, err := strconv.Atoi(indexStr)
		if err != nil {
			return ret, err
		}
		ret = append(ret, index)
	}
	return ret, nil
}

func ShouldRetry(err error) bool {
	return apierr.IsConflict(err) || apierr.IsServerTimeout(err)
}
