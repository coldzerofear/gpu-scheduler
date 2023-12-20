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
package algorithm

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/klog"

	"tkestack.io/gpu-admission/pkg/device"
	"tkestack.io/gpu-admission/pkg/util"
)

type allocator struct {
	nodeInfo *device.NodeInfo
}

func NewAllocator(n *device.NodeInfo) *allocator {
	return &allocator{nodeInfo: n}
}

// IsAllocatable attempt to allocate containers which has GPU request of given pod
//func (alloc *allocator) IsAllocatable(pod *v1.Pod) bool {
//	allocatable := true
//	for _, c := range pod.Spec.Containers {
//		if !util.IsGPURequiredContainer(&c) {
//			continue
//		}
//		_, err := alloc.AllocateOne(&c)
//		if err != nil {
//			klog.Infof("failed to allocate for pod %s container %s", pod.UID, c.Name)
//			allocatable = false
//			break
//		}
//	}
//	return allocatable
//}

// Allocate tries to find a suitable GPU device for containers
// and records some data in pod's annotation
// 试图为容器找到合适的GPU设备，并在pod的注释中记录一些数据
func (alloc *allocator) Allocate(pod *v1.Pod) (*v1.Pod, error) {
	newPod := pod.DeepCopy()
	if newPod.Annotations == nil {
		newPod.Annotations = make(map[string]string)
	}
	for i, c := range newPod.Spec.Containers {
		// 跳过不需要分配gpu的容器
		if !util.IsGPURequiredContainer(&c) {
			continue
		}
		devIDs := []string{}
		devs, err := alloc.AllocateOne(&c, newPod)
		if err != nil {
			klog.Infof("failed to allocate for pod %s(%s)", newPod.Name, c.Name)
			return nil, err
		}
		for _, dev := range devs {
			devIDs = append(devIDs, strconv.Itoa(dev.GetID()))
		}
		newPod.Annotations[util.PredicateGPUIndexPrefix+strconv.Itoa(i)] = strings.Join(devIDs, ",")
	}
	// 标记分配的节点
	newPod.Annotations[util.PredicateNode] = alloc.nodeInfo.GetName()
	// 标记还没分配完成，需要在设备插件阶段进行下一阶段的分配
	newPod.Annotations[util.GPUAssigned] = "false"
	// 标记调度分配时间
	newPod.Annotations[util.PredicateTimeAnnotation] = fmt.Sprintf("%d", time.Now().UnixNano())

	return newPod, nil
}

// AllocateOne tries to allocate GPU devices for given container
// 为容器预分配gpu设备，并返回设备信息
func (alloc *allocator) AllocateOne(container *v1.Container, pod *v1.Pod) ([]*device.DeviceInfo, error) {
	var (
		devs           []*device.DeviceInfo
		sharedMode     bool
		vcore, vmemory uint
	)
	// 当前节点
	node := alloc.nodeInfo.GetNode()

	//// 查找节点中内存总资源量
	//nodeTotalMemory := util.GetCapacityOfNode(node, util.VMemoryAnnotation)
	//// 查找节点中总gpu数量
	//deviceCount := util.GetGPUDeviceCountOfNode(node)
	//// TODO 节点设备总内存 / 节点设备数 = 单设备内存容量, 需要改造考虑 节点设备混用情况
	//deviceTotalMemory := uint(nodeTotalMemory / deviceCount)

	// 容器请求的核心数
	needCores := util.GetGPUResourceOfContainer(container, util.VCoreAnnotation)
	// 容器请求的显存数
	needMemory := util.GetGPUResourceOfContainer(container, util.VMemoryAnnotation)

	switch {
	case needCores < util.HundredCore:
		// 核心数请求小于100的为共享模式,
		devs = NewShareMode(alloc.nodeInfo, pod).Evaluate(needCores, needMemory)
		sharedMode = true
	default:
		// 请求核心大于100为独占模式
		devs = NewExclusiveMode(alloc.nodeInfo, pod).Evaluate(needCores, needMemory)
	}

	if len(devs) == 0 {
		return nil, fmt.Errorf("failed to allocate for container %s", container.Name)
	}

	//if sharedMode {
	//	vcore = needCores
	//	vmemory = needMemory
	//} else {
	//	vcore = util.HundredCore
	//	vmemory = deviceTotalMemory
	//}

	// record this container GPU request, we don't rollback data if an error happened,
	// because any container failed to be allocated will cause the predication failed
	for _, dev := range devs {
		//err := alloc.nodeInfo.AddUsedResources(dev.GetID(), vcore, vmemory)
		//if err != nil {
		//	klog.Infof("failed to update used resource for node %s dev %d due to %v",
		//		node.Name, dev.GetID(), err)
		//	return nil, err
		//}
		if sharedMode {
			if err := alloc.nodeInfo.AddUsedResources(dev.GetID(), vcore, vmemory); err != nil {
				klog.Infof("failed to update used resource for node %s dev %d due to %v",
					node.Name, dev.GetID(), err)
				return nil, err
			}
		} else {
			if err := alloc.nodeInfo.AddUsedResources(dev.GetID(), util.HundredCore, dev.GetTotalMemory()); err != nil {
				klog.Infof("failed to update used resource for node %s dev %d due to %v",
					node.Name, dev.GetID(), err)
				return nil, err
			}
		}
	}
	return devs, nil
}
