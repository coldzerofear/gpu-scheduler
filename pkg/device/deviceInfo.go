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
	"encoding/json"
	"fmt"
	v1 "k8s.io/api/core/v1"

	"tkestack.io/gpu-admission/pkg/util"
)

type DeviceInfo struct {
	id          int
	totalMemory uint
	usedMemory  uint
	usedCore    uint
	// TODO 新增设备名
	name string
	// TODO 新增mig识别
	isMig bool
	// TODO 新增设备健康
	health bool
	// TODO 新增算力等级
	capability int
}

// Deprecated
func newDeviceInfo(id int, totalMemory uint) *DeviceInfo {
	return &DeviceInfo{
		id:          id,
		totalMemory: totalMemory,
	}
}

type GPUInfo struct {
	Id         string `json:"id,omitempty"`
	Core       int    `json:"core,omitempty"`
	Memory     uint   `json:"memory,omitempty"`
	Type       string `json:"type,omitempty"`
	IsMig      bool   `json:"isMig,omitempty"`
	Capability int    `json:"capability,omitempty"`
	Health     bool   `json:"health"`
}

func newDeviceInfoMapByNode(node *v1.Node) (map[int]*DeviceInfo, error) {
	deviceMap := make(map[int]*DeviceInfo)
	gpuInfosJsonStr := node.Annotations[util.NodeAnnotationDeviceRegister]
	var gpuInfos = []GPUInfo{}
	err := json.Unmarshal([]byte(gpuInfosJsonStr), &gpuInfos)
	if err != nil {
		return deviceMap, err
	}
	for i, info := range gpuInfos {
		deviceMap[i] = &DeviceInfo{
			id:          i,
			name:        info.Type,
			totalMemory: info.Memory,
			isMig:       info.IsMig,
			health:      info.Health,
			capability:  info.Capability,
		}
	}
	return deviceMap, nil
}

// GetID returns the idx of this device
func (dev *DeviceInfo) GetID() int {
	return dev.id
}

// IsMig returns the isMig of this device
func (dev *DeviceInfo) IsMig() bool {
	return dev.isMig
}

// IsHealth returns the health of this device
func (dev *DeviceInfo) IsHealth() bool {
	return dev.health
}

// GetComputeCapability returns the capability of this device
func (dev *DeviceInfo) GetComputeCapability() int {
	return dev.capability
}

// GetName returns the name of this device
func (dev *DeviceInfo) GetName() string {
	return dev.name
}

// GetTotalMemory returns the totalMemory of this device
func (dev *DeviceInfo) GetTotalMemory() uint {
	return dev.totalMemory
}

// AddUsedResources records the used GPU core and memory
func (dev *DeviceInfo) AddUsedResources(usedCore uint, usedMemory uint) error {
	if usedCore+dev.usedCore > util.HundredCore {
		return fmt.Errorf("update usedcore failed, request: %d, already used: %d",
			usedCore, dev.usedCore)
	}

	if usedMemory+dev.usedMemory > dev.totalMemory {
		return fmt.Errorf("update usedmemory failed, request: %d, already used: %d",
			usedMemory, dev.usedMemory)
	}

	dev.usedCore += usedCore
	dev.usedMemory += usedMemory

	return nil
}

// AllocatableCores returns the remaining cores of this GPU device
func (d *DeviceInfo) AllocatableCores() uint {
	return util.HundredCore - d.usedCore
}

// AllocatableMemory returns the remaining memory of this GPU device
func (d *DeviceInfo) AllocatableMemory() uint {
	return d.totalMemory - d.usedMemory
}
