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
package predicate

import (
	"context"
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"

	"tkestack.io/gpu-admission/pkg/algorithm"
	"tkestack.io/gpu-admission/pkg/device"
	"tkestack.io/gpu-admission/pkg/util"
)

type GPUFilter struct {
	kubeClient kubernetes.Interface
	nodeLister listerv1.NodeLister
	podLister  listerv1.PodLister
	podIndexer cache.Indexer
}

const (
	NAME          = "GPUPredicate"
	PodIndexerKey = "spec.nodeName"
	PodPhaseField = "status.phase"
	waitTimeout   = 10 * time.Second
)

func NewGPUFilter1(client kubernetes.Interface, stopCh chan struct{}) (*GPUFilter, error) {
	informerFactory := kubeinformers.NewSharedInformerFactory(client, time.Second*30)
	nodeInformer := informerFactory.Core().V1().Nodes().Informer()
	pod := &corev1.Pod{}
	podInformer := informerFactory.InformerFor(pod, func(k kubernetes.Interface, duration time.Duration) cache.SharedIndexInformer {
		return cache.NewSharedIndexInformer(
			cache.NewFilteredListWatchFromClient(
				k.CoreV1().RESTClient(),
				"pods",
				metav1.NamespaceAll,
				func(options *metav1.ListOptions) {
					options.FieldSelector = fields.OneTermNotEqualSelector(PodPhaseField, string(corev1.PodSucceeded)).String()
				}),
			pod,
			duration,
			cache.Indexers{})
	})
	gpuFilter := &GPUFilter{
		kubeClient: client,
		nodeLister: listerv1.NewNodeLister(nodeInformer.GetIndexer()),
		podLister:  listerv1.NewPodLister(podInformer.GetIndexer()),
	}
	// 启动Informer
	go informerFactory.Start(stopCh)

	return gpuFilter, nil
}

func NewFakeGPUFilter() (*GPUFilter, error) {
	return &GPUFilter{}, nil
}

func NewGPUFilter(client kubernetes.Interface) (*GPUFilter, error) {

	// 创建nodeInformerFactory，设置每30s同步一次，监听所有的节点信息
	nodeInformerFactory := kubeinformers.NewSharedInformerFactory(client, time.Second*30)
	// 监听条件:过滤掉状态已执行成功的pod
	podListOptions := func(options *metav1.ListOptions) {
		options.FieldSelector = fmt.Sprintf("%s!=%s", PodPhaseField, corev1.PodSucceeded)
	}
	// 创建podInformerInformerFactory，设置每30s同步一次，监听所有的命名空间下没有被调度的pod
	podInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(client,
		time.Second*30, kubeinformers.WithNamespace(metav1.NamespaceAll),
		kubeinformers.WithTweakListOptions(podListOptions))
	// 构建出GPUFilter，将node和pod Informer的监听到的Lister，作为成员变量，方便在过滤阶段获取
	nodeInformer := nodeInformerFactory.Core().V1().Nodes()
	podInformer := podInformerFactory.Core().V1().Pods()
	// TODO 为pod节点添加索引
	if err := podInformer.Informer().AddIndexers(cache.Indexers{
		PodIndexerKey: func(obj interface{}) ([]string, error) {
			indexerKeys := []string{}
			if pod, ok := obj.(*corev1.Pod); ok {
				indexerKeys = append(indexerKeys, pod.Spec.NodeName)
			}
			return indexerKeys, nil
		},
	}); err != nil {
		return nil, err
	}
	gpuFilter := &GPUFilter{
		kubeClient: client,
		nodeLister: nodeInformer.Lister(),
		podLister:  podInformer.Lister(),
		podIndexer: podInformer.Informer().GetIndexer(),
	}
	// 启动相关Informer
	go nodeInformerFactory.Start(nil)
	go podInformerFactory.Start(nil)
	klog.Info("starting informers: waiting sync……")
	for {
		Ready := true
		if !nodeInformer.Informer().HasSynced() {
			Ready = false
		}
		if !podInformer.Informer().HasSynced() {
			Ready = false
		}
		if Ready {
			klog.Info("informers is ready!")
			break
		}
		time.Sleep(100 * time.Microsecond)
	}
	return gpuFilter, nil
}

func (gpuFilter *GPUFilter) Name() string {
	return NAME
}

type filterFunc func(*corev1.Pod, []corev1.Node) ([]corev1.Node, extenderv1.FailedNodesMap, error)

func (gpuFilter *GPUFilter) FilterOnCache(args extenderv1.ExtenderArgs) *extenderv1.ExtenderFilterResult {
	// 在filter阶段接收调度器发过来的参数ExtenderArgs
	// 在此使用的模式是 nodeCacheCapable = true，开启调度器缓存，增强调度器吞吐量
	// 判断当前pod是否 请求了gpu, 非gpu pod不走额外调度
	if !util.IsGPURequiredPod(args.Pod) {
		// 当前pod没有携带gpu有效参数，则直接返回
		return &extenderv1.ExtenderFilterResult{
			NodeNames:   args.NodeNames,
			FailedNodes: nil,
			Error:       "",
		}
	}

	filters := []filterFunc{
		// TODO 节点心跳过滤
		gpuFilter.heartbeatFilter,
		// TODO 设备类型标签预过滤
		gpuFilter.labelsFilter,
		// 设备过滤
		// deviceFilter将选择一个且只有一个节点来填充请求，因此它应该始终是gpuFilter的最后一个筛选器
		gpuFilter.deviceFilter,
	}
	filteredNodes, failedNodesMap := gpuFilter.getNodesOnCache(*args.NodeNames...)
	// 遍历执行每一个上面定义的过滤函数
	for _, filter := range filters {
		// 通过过滤函数得到成功者列表和失败者表
		passedNodes, failedNodes, err := filter(args.Pod, filteredNodes)
		if err != nil {
			// 当过滤途中出现错误，则将错误返回给调度器
			return &extenderv1.ExtenderFilterResult{
				Error: err.Error(),
			}
		}
		// 变更最新的节点过滤列表以进行下一轮过滤
		filteredNodes = passedNodes
		// 总结失败者名单和失败原因
		for name, reason := range failedNodes {
			failedNodesMap[name] = reason
		}
	}
	nodeNames := make([]string, len(filteredNodes))
	for i, node := range filteredNodes {
		nodeNames[i] = node.Name
	}
	// 返回过滤结果
	return &extenderv1.ExtenderFilterResult{
		NodeNames:   &nodeNames,
		FailedNodes: failedNodesMap,
		Error:       "",
	}
}

func (gpuFilter *GPUFilter) getNodesOnCache(nodeNames ...string) ([]corev1.Node, extenderv1.FailedNodesMap) {
	failedNodesMap := make(extenderv1.FailedNodesMap)
	nodes := make([]corev1.Node, 0)
	for _, nodeName := range nodeNames {
		if node, err := gpuFilter.nodeLister.Get(nodeName); err != nil {
			failedNodesMap[nodeName] = fmt.Sprintf("error finding node name %s from cache, err: %v", nodeName, err)
		} else {
			nodes = append(nodes, *node.DeepCopy())
		}
	}
	return nodes, failedNodesMap
}

// 实现过滤接口
func (gpuFilter *GPUFilter) Filter(args extenderv1.ExtenderArgs) *extenderv1.ExtenderFilterResult {
	// 在filter阶段接收调度器发过来的参数ExtenderArgs
	// 在此使用的模式是 nodeCacheCapable = false，没有开启node缓存模式，所以ExtenderArgs将携带完整的NodeList参数
	// TODO 当在大规模集群使用的时候，可以考虑对此改造，减少大参数的传递，增强性能
	// 判断当前pod是否 请求了gpu
	if !util.IsGPURequiredPod(args.Pod) {
		// 当前pod没有携带gpu有效参数，则直接返回
		return &extenderv1.ExtenderFilterResult{
			Nodes:       args.Nodes,
			FailedNodes: nil,
			Error:       "",
		}
	}

	filters := []filterFunc{
		// TODO 节点心跳过滤
		gpuFilter.heartbeatFilter,
		// TODO 设备类型标签预过滤
		gpuFilter.labelsFilter,
		// 设备过滤
		// deviceFilter将选择一个且只有一个节点来填充请求，因此它应该始终是gpuFilter的最后一个筛选器
		gpuFilter.deviceFilter,
	}
	// 待过滤的节点
	filteredNodes := args.Nodes.Items
	failedNodesMap := make(extenderv1.FailedNodesMap)
	// 遍历执行每一个上面定义的过滤函数
	for _, filter := range filters {
		// 通过过滤函数得到成功者列表和失败者表
		passedNodes, failedNodes, err := filter(args.Pod, filteredNodes)
		if err != nil {
			// 当过滤途中出现错误，则将错误返回给调度器
			return &extenderv1.ExtenderFilterResult{
				Error: err.Error(),
			}
		}
		// 变更最新的节点过滤列表以进行下一轮过滤
		filteredNodes = passedNodes
		// 总结失败者名单和失败原因
		for name, reason := range failedNodes {
			failedNodesMap[name] = reason
		}
	}
	// 返回过滤结果
	return &extenderv1.ExtenderFilterResult{
		Nodes: &corev1.NodeList{
			Items: filteredNodes,
		},
		FailedNodes: failedNodesMap,
		Error:       "",
	}
}

// TODO 节点组件心跳过滤： 过滤掉心跳超时5分钟的节点
func (gpuFilter *GPUFilter) heartbeatFilter(pod *corev1.Pod, nodes []corev1.Node) ([]corev1.Node, extenderv1.FailedNodesMap, error) {
	var (
		// 过滤成功的节点
		filteredNodes = make([]corev1.Node, 0)
		// 过滤掉的失败节点
		failedNodesMap = make(extenderv1.FailedNodesMap)
	)
	for _, node := range nodes {
		if val, ok := node.Annotations[util.NodeAnnotationHeartbeat]; ok {
			nano, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				failedNodesMap[node.Name] = "node heartbeat time is not a standard timestamp"
				continue
			}
			heartbeatTime := time.Unix(0, nano)
			if time.Since(heartbeatTime) > 1*time.Minute {
				failedNodesMap[node.Name] = "node heartbeat timeout"
			} else {
				filteredNodes = append(filteredNodes, node)
			}
		} else {
			failedNodesMap[node.Name] = "node has no gpu manager heartbeat"
		}
	}
	return filteredNodes, failedNodesMap, nil
}

// TODO 当pod指定gpu设备类型时进行节点标签预过滤
func (gpuFilter *GPUFilter) labelsFilter(pod *corev1.Pod, nodes []corev1.Node) ([]corev1.Node, extenderv1.FailedNodesMap, error) {
	var (
		// 成功调度的节点
		filteredNodes = make([]corev1.Node, 0)
		// 失败的节点
		failedNodesMap = make(extenderv1.FailedNodesMap)
	)

	use, ok1 := pod.Annotations[util.PodAnnotationUseGpuType]
	unuse, ok2 := pod.Annotations[util.PodAnnotationUnUseGpuType]
	// 当pod没有指定设备类型则跳过过滤
	if !ok1 && !ok2 {
		return nodes, failedNodesMap, nil
	}

	for _, node := range nodes {
		match := true
		if model, ok := node.Labels[util.GPUModelLabel]; ok {
			types := strings.Split(model, ",")
			if ok1 {
				useTypes := strings.Split(use, ",")
				// TODO 这里使用anyMatch的模式
				// 节点上只要有一个设备类型匹配上 usetype 就算匹配通过
				match = util.ContainsSliceFunc(types, func(typeName string) bool {
					typeName = strings.ToUpper(typeName)
					return util.ContainsSliceFunc(useTypes, func(useType string) bool {
						return strings.Contains(typeName, strings.ToUpper(useType))
					})
				})
			}
			if ok2 && match {
				match = false
				unuseTypes := strings.Split(unuse, ",")
				// TODO 这里使用allMacth的模式,
				// 如果节点上的 type 全部匹配上了 unuseType 则过滤该节点
				for _, typeName := range types {
					typeName = strings.ToUpper(typeName)
					if !util.ContainsSliceFunc(unuseTypes, func(unuseType string) bool {
						return strings.Contains(typeName, strings.ToUpper(unuseType))
					}) {
						match = true
						break
					}
				}
			}
		}
		if match {
			// 当节点没有相关标签，则放行，在gpuFilter过滤阶段再次进行匹配过滤
			filteredNodes = append(filteredNodes, node)
		} else {
			failedNodesMap[node.Name] = "node does not have a matching gpu type"
		}
	}
	return filteredNodes, failedNodesMap, nil
}

// deviceFilter will choose one and only one node fullfil the request,
// so it should always be the last filter of gpuFilter
func (gpuFilter *GPUFilter) deviceFilter(
	pod *corev1.Pod, nodes []corev1.Node) ([]corev1.Node, extenderv1.FailedNodesMap, error) {
	// #lizard forgives
	var (
		// 过滤成功的节点
		filteredNodes = make([]corev1.Node, 0)
		// 过滤掉的失败节点
		failedNodesMap = make(extenderv1.FailedNodesMap)
		nodeInfoList   []*device.NodeInfo
		success        bool
		// 排序算法规则, 底层调用快排
		sorter = device.NodeInfoSort(
			//按可分配内核数量比较两个设备或节点
			device.ByAllocatableCores,
			//按可分配内存余量比较两个设备或节点
			device.ByAllocatableMemory,
			//按节点名称或gpu设备id排序
			device.ByID)
	)
	// 检查当前pod是否已分配gpu
	for k := range pod.Annotations {
		// 当pod存在以下注解表示已分配过gpu, 不需要再次分配
		if strings.Contains(k, util.GPUAssigned) ||
			strings.Contains(k, util.PredicateTimeAnnotation) ||
			strings.Contains(k, util.PredicateGPUIndexPrefix) {
			return filteredNodes, failedNodesMap, fmt.Errorf("pod %s had been predicated!", pod.Name)
		}
	}
	// node过滤和节点分配状态构建
	for i := range nodes {
		node := &nodes[i]
		// 通过检查node的Capacity判断该node节点是否存在gpu设备
		// 过滤掉非gpu节点
		if !util.IsGPUEnabledNode(node) {
			failedNodesMap[node.Name] = "no GPU device"
			continue
		}
		// 找到调度到当前节点上并已经成功运行的pod
		pods, err := gpuFilter.ListPodsOnNode(node)
		if err != nil {
			failedNodesMap[node.Name] = "failed to get pods on node"
			continue
		}
		// 根据节点上运行的pod，构建节点设备分配状态
		nodeInfo, err := device.NewNodeInfo(node, pods)
		if err != nil {
			klog.Warningf("new node info error, skipping node: %s, err: %v", node.Name, err)
			continue
		}
		nodeInfoList = append(nodeInfoList, nodeInfo)
	}
	// 将构建的节点状态按规则排序: 节点资源从小到大排序 企图找到正好满足请求的最小节点
	sorter.Sort(nodeInfoList)
	// 遍历排序后的节点信息
	for _, nodeInfo := range nodeInfoList {
		node := nodeInfo.GetNode()
		if success {
			// success 标记已为pod分配了一个节点，将当前节点加入到失败者表，并不再走下面的流程
			failedNodesMap[node.Name] = fmt.Sprintf(
				"pod %s has already been matched to another node", pod.UID)
			continue
		}

		alloc := algorithm.NewAllocator(nodeInfo)
		// 尝试给pod分配gpu资源，分配出错则跳过节点
		newPod, err := alloc.Allocate(pod)
		if err != nil {
			failedNodesMap[node.Name] = fmt.Sprintf(
				"pod %s does not match with this node", pod.UID)
			continue
		} else {
			annotationMap := make(map[string]string)
			for k, v := range newPod.Annotations {
				if strings.Contains(k, util.GPUAssigned) ||
					strings.Contains(k, util.PredicateTimeAnnotation) ||
					strings.Contains(k, util.PredicateGPUIndexPrefix) ||
					strings.Contains(k, util.PredicateNode) {
					annotationMap[k] = v
				}
			}
			// 将分配的gpu信息以anntation的方式patch给pod
			err := gpuFilter.patchPodWithAnnotations(newPod, annotationMap)
			if err != nil {
				// patch以失败告终，则将当前节点加入失败者表
				failedNodesMap[node.Name] = "update pod annotation failed"
				continue
			}
			// patch成功将当前节点加入成功者列表
			filteredNodes = append(filteredNodes, *node)
			// 并标记成功
			success = true
		}
	}

	return filteredNodes, failedNodesMap, nil
}

func (gpuFilter *GPUFilter) listPodsByIndexer(indexerKey string, indexedValues ...string) ([]*corev1.Pod, error) {
	pods := make([]*corev1.Pod, 0)
	if len(indexerKey) == 0 || len(indexedValues) == 0 {
		return pods, fmt.Errorf("at least one indexer condition")
	}
	for _, value := range indexedValues {
		podObjs, err := gpuFilter.podIndexer.ByIndex(indexerKey, value)
		if err != nil {
			return pods, err
		}
		for _, obj := range podObjs {
			pods = append(pods, obj.(*corev1.Pod))
		}
	}
	return pods, nil
}

func (gpuFilter *GPUFilter) ListPodsOnNode(node *corev1.Node) ([]*corev1.Pod, error) {
	// #lizard forgives
	// 从缓存中查询所有的pod
	//pods, err := gpuFilter.podLister.Pods(corev1.NamespaceAll).List(labels.Everything())
	//if err != nil {
	//	return nil, err
	//}
	// TODO 改造为利用索引查询当前节点的pod
	pods, err := gpuFilter.listPodsByIndexer(PodIndexerKey, node.Name, "")
	if err != nil {
		return nil, err
	}

	var ret []*corev1.Pod
	for _, pod := range pods {
		klog.V(9).Infof("List pod %s", pod.Name)
		var predicateNode string
		// 寻找谓词节点注释
		if pod.Spec.NodeName == "" && pod.Annotations != nil {
			if v, ok := pod.Annotations[util.PredicateNode]; ok {
				predicateNode = v
			}
		}
		// 找到调度到该节点上的pod
		if (pod.Spec.NodeName == node.Name || predicateNode == node.Name) &&
			pod.Status.Phase != corev1.PodSucceeded &&
			pod.Status.Phase != corev1.PodFailed {
			ret = append(ret, pod)
			klog.V(9).Infof("get pod %s on node %s", pod.UID, node.Name)
		}
	}
	return ret, nil
}

func (gpuFilter *GPUFilter) patchPodWithAnnotations(
	pod *corev1.Pod, annotationMap map[string]string) error {
	// update annotations by patching to the pod
	type patchMetadata struct {
		Annotations map[string]string `json:"annotations"`
	}
	type patchPod struct {
		Metadata patchMetadata `json:"metadata"`
	}
	payload := patchPod{
		Metadata: patchMetadata{
			Annotations: annotationMap,
		},
	}
	payloadBytes, _ := json.Marshal(payload)
	// PollImmediate尝试条件函数，持续循环直到返回true、错误或超时停止
	// 每秒重试、 等待10秒
	err := wait.PollImmediate(time.Second, waitTimeout, func() (bool, error) {
		_, err := gpuFilter.kubeClient.CoreV1().Pods(pod.Namespace).
			Patch(context.Background(), pod.Name, k8stypes.StrategicMergePatchType, payloadBytes, metav1.PatchOptions{})
		// patch成功不需要重试
		if err == nil {
			return true, nil
		}
		// patch失败需要重试
		if util.ShouldRetry(err) {
			return false, nil
		}
		// 失败，不再重试
		return false, err
	})
	if err != nil {
		msg := fmt.Sprintf("failed to add annotation %v to pod %s due to %s",
			annotationMap, pod.UID, err.Error())
		klog.Infof(msg)
		return fmt.Errorf(msg)
	}
	return nil
}
