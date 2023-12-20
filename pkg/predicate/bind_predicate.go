package predicate

import (
	"context"
	"encoding/json"
	"fmt"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
	"time"
	"tkestack.io/gpu-admission/pkg/util"
	"tkestack.io/gpu-admission/pkg/util/nodelock"
)

type NodeBinding struct {
	kubeClient kubernetes.Interface
}

func NewNodeBinding(client kubernetes.Interface) (*NodeBinding, error) {
	return &NodeBinding{client}, nil
}

func (b *NodeBinding) Name() string {
	return "BindPredicate"
}

// TODO 在绑定节点前，尝试对节点加锁
func (b *NodeBinding) Bind(args extenderv1.ExtenderBindingArgs) *extenderv1.ExtenderBindingResult {
	klog.InfoS("Bind", "pod", args.PodName, "namespace", args.PodNamespace, "podUID", args.PodUID, "node", args.Node)
	var err error
	var res *extenderv1.ExtenderBindingResult
	binding := &v1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: args.PodName, UID: args.PodUID},
		Target:     v1.ObjectReference{Kind: "Node", Name: args.Node},
	}
	// 节点加锁
	if err = nodelock.LockNode(args.Node, b.kubeClient); err != nil {
		// TODO 加锁失败
		klog.ErrorS(err, "Failed to lock node", "node", args.Node)
		time.Sleep(100 * time.Millisecond) // 加锁失败尽量错开调度时间
	}
	// TODO　释放锁的时机不再调度器阶段，在设备插件阶段进行释放
	//defer util.ReleaseNodeLock(args.Node)
	type patchMetadata struct {
		Labels map[string]string `json:"labels,omitempty"`
	}
	type patchLable struct {
		Metadata patchMetadata `json:"metadata"`
	}
	patch := patchLable{
		Metadata: patchMetadata{
			Labels: map[string]string{
				util.PodLabelBindTime:        fmt.Sprintf("%d", time.Now().UnixNano()),
				util.PodLabelDeviceBindPhase: string(util.DeviceBindAllocating),
			},
		},
	}
	bytes, _ := json.Marshal(patch)
	ctx := context.Background()
	_, err = b.kubeClient.CoreV1().Pods(args.PodNamespace).Patch(
		ctx, args.PodName, types.StrategicMergePatchType, bytes, metav1.PatchOptions{})
	if err != nil {
		// patch 失败不影响下面banding
		klog.ErrorS(err, "patch pod annotation failed")
	}
	if err = b.kubeClient.CoreV1().Pods(args.PodNamespace).Bind(ctx, binding, metav1.CreateOptions{}); err != nil {
		klog.ErrorS(err, "Failed to bind pod", "pod", args.PodName, "namespace", args.PodNamespace, "podUID", args.PodUID, "node", args.Node)
	}
	if err == nil {
		res = &extenderv1.ExtenderBindingResult{
			Error: "",
		}
	} else {
		res = &extenderv1.ExtenderBindingResult{
			Error: err.Error(),
		}
	}
	klog.Infoln("After Binding Process")
	return res
}
