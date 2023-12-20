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
package device

import (
	"sort"

	"k8s.io/api/core/v1"
	"k8s.io/klog"

	"tkestack.io/gpu-admission/pkg/util"
)

type NodeInfo struct {
	name        string
	node        *v1.Node
	devs        map[int]*DeviceInfo // gpu设备信息
	deviceCount int                 // 可用设备数
	totalMemory uint                // 总可用内存
	usedCore    uint                // 已使用核心
	usedMemory  uint                // 已使用内存
	// 新增节点最大算力等级单位
	maxCapability int
}

func NewNodeInfo(node *v1.Node, pods []*v1.Pod) (*NodeInfo, error) {
	klog.V(4).Infof("debug: NewNodeInfo() creates nodeInfo for %s", node.Name)
	//devMap := map[int]*DeviceInfo{}
	// 当前节点的总显存容量
	nodeTotalMemory := uint(util.GetCapacityOfNode(node, util.VMemoryAnnotation))
	//// 当前节点上的gpu总数
	//deviceCount := util.GetGPUDeviceCountOfNode(node)
	//
	//// 显存总量 / gpu数量 得到 单个gpu的显存总量
	//// TODO 这里默认按照 设备数 平均 每个设备的内存，没有考虑节点是否存在设备混用情况，需要改造
	//deviceTotalMemory := nodeTotalMemory / uint(deviceCount)
	//for i := 0; i < deviceCount; i++ {
	//	devMap[i] = newDeviceInfo(i, deviceTotalMemory)
	//}

	devMap, err := newDeviceInfoMapByNode(node)
	if err != nil {
		return nil, err
	}
	deviceCount := len(devMap)

	maxCapability := 0
	for _, info := range devMap {
		maxCapability = util.Max(maxCapability, info.capability)
	}

	// 当前节点的gpu设备信息
	ret := &NodeInfo{
		name:          node.Name,
		node:          node,
		devs:          devMap,
		deviceCount:   deviceCount,
		totalMemory:   nodeTotalMemory,
		maxCapability: maxCapability,
	}

	// 根据pod的注释，构建节点分配状态
	// According to the pods' annotations, construct the node allocation
	// state
	for _, pod := range pods {
		// 遍历pod的所有容器
		for i, c := range pod.Spec.Containers {
			// 根据容器的索引得到分配给该容器的 gpu设备索引
			predicateIndexes, err := util.GetPredicateIdxOfContainer(pod, i)
			if err != nil {
				continue
			}
			// 遍历gpu设备索引
			for _, index := range predicateIndexes {
				var vcore, vmemory uint
				// 当索引号 大于等于 节点上的gpu总设备数则跳过
				if index >= deviceCount {
					klog.Infof("invalid predicateIndex %d larger than device count", index)
					continue
				}
				// 获取容器请求的cuda核数
				vcore = util.GetGPUResourceOfContainer(&c, util.VCoreAnnotation)
				if vcore < util.HundredCore {
					// 当 请求的cuda核数小于1张完整的卡，则 获取请求的 显存大小
					vmemory = util.GetGPUResourceOfContainer(&c, util.VMemoryAnnotation)
				} else {
					// 当请求的cuda核数为完整的1张或多张卡，则完整分配gpu所有的显存
					vcore = util.HundredCore
					//vmemory = deviceTotalMemory
					vmemory = devMap[index].totalMemory
				}
				// 记录使用的GPU内核和显存
				err = ret.AddUsedResources(index, vcore, vmemory)
				if err != nil {
					klog.Infof("failed to update used resource for node %s dev %d due to %v",
						node.Name, index, err)
				}
			}

		}
	}

	return ret, nil
}

// AddUsedResources records the used GPU core and memory
func (n *NodeInfo) AddUsedResources(devID int, vcore uint, vmemory uint) error {
	err := n.devs[devID].AddUsedResources(vcore, vmemory)
	if err != nil {
		klog.Infof("failed to update used resource for node %s dev %d due to %v", n.name, devID, err)
		return err
	}
	n.usedCore += vcore
	n.usedMemory += vmemory
	return nil
}

// GetDeviceCount returns the number of GPU devices
func (n *NodeInfo) GetDeviceCount() int {
	return n.deviceCount
}

// GetDeviceMap returns each GPU device information structure
func (n *NodeInfo) GetDeviceMap() map[int]*DeviceInfo {
	return n.devs
}

// GetNode returns the original node structure of kubernetes
func (n *NodeInfo) GetNode() *v1.Node {
	return n.node
}

// GetMaxCapability returns the maxCapability of GPU devices
func (n *NodeInfo) GetMaxCapability() int {
	return n.maxCapability
}

// GetName returns node name
func (n *NodeInfo) GetName() string {
	return n.name
}

// GetAvailableCore returns the remaining cores of this node
func (n *NodeInfo) GetAvailableCore() int {
	return n.deviceCount*util.HundredCore - int(n.usedCore)
}

// GetAvailableMemory returns the remaining memory of this node
func (n *NodeInfo) GetAvailableMemory() int {
	return int(n.totalMemory - n.usedMemory)
}

type nodeInfoPriority struct {
	data []*NodeInfo
	less []LessFunc
}

func NodeInfoSort(less ...LessFunc) *nodeInfoPriority {
	return &nodeInfoPriority{
		less: less,
	}
}

func (nip *nodeInfoPriority) Sort(data []*NodeInfo) {
	nip.data = data
	sort.Sort(nip)
}

func (nip *nodeInfoPriority) Len() int {
	return len(nip.data)
}

func (nip *nodeInfoPriority) Swap(i, j int) {
	nip.data[i], nip.data[j] = nip.data[j], nip.data[i]
}

func (nip *nodeInfoPriority) Less(i, j int) bool {
	var k int

	for k = 0; k < len(nip.less)-1; k++ {
		less := nip.less[k]
		switch {
		case less(nip.data[i], nip.data[j]):
			return true
		case less(nip.data[j], nip.data[i]):
			return false
		}
	}

	return nip.less[k](nip.data[i], nip.data[j])
}
