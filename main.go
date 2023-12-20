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
package main

import (
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"strings"

	"github.com/julienschmidt/httprouter"
	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/logs"
	"k8s.io/klog"

	"tkestack.io/gpu-admission/pkg/predicate"
	"tkestack.io/gpu-admission/pkg/route"
	"tkestack.io/gpu-admission/pkg/version/verflag"
)

var (
	kubeconfig              string
	masterURL               string
	listenAddress           string
	profileAddress          string
	nodeCacheCapable        bool
	enableComputeCapability bool
)

func main() {
	klog.InitFlags(nil)
	// 加载命令行参数默认值
	addFlags(pflag.CommandLine)
	//Parse解析参数列表中的标志定义，其中不应包含命令名。 必须在定义FlagSet中的所有标志之后以及程序访问标志之前调用。如果设置了-help或-h但未定义，则返回值将为ErrHelp。
	flag.CommandLine.Parse([]string{})
	// 初始化日志参数
	initFlags()
	logs.InitLogs()
	defer logs.FlushLogs()
	verflag.PrintAndExitIfRequested()

	// 创建http路由
	router := httprouter.New()
	// 添加获取当前 版本接口
	route.AddVersion(router)

	var (
		clientCfg *rest.Config
		err       error
	)
	// 获取配置k8s客户端配置文件
	clientCfg, err = clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if err != nil {
		klog.Fatalf("Error building kubeconfig: %s", err.Error())
	}
	// 创建k8s客户端
	kubeClient, err := kubernetes.NewForConfig(clientCfg)
	if err != nil {
		klog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	//quit := make(chan struct{})
	//defer close(quit)
	//gpuFilter1, err := predicate.NewGPUFilter1(kubeClient, quit)

	// 创建gpu 过滤接口，并将其加入http路由
	gpuFilter, err := predicate.NewGPUFilter(kubeClient)
	if err != nil {
		klog.Fatalf("Failed to new gpu quota filter: %s", err.Error())
	}
	// 添加filter接口
	route.AddFilterPredicate(router, gpuFilter, nodeCacheCapable)

	nodeBinding, _ := predicate.NewNodeBinding(kubeClient)
	// 添加bind接口
	route.AddBindPredicate(router, nodeBinding)

	go func() {
		log.Println(http.ListenAndServe(profileAddress, nil))
	}()

	klog.Infof("Server starting on %s", listenAddress)
	// 创建服务器并监听请求
	if err := http.ListenAndServe(listenAddress, router); err != nil {
		log.Fatal(err)
	}
}

func addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&kubeconfig, "kubeconfig", "",
		"Path to a kubeconfig. Only required if out-of-cluster.")
	fs.StringVar(&masterURL, "master", "",
		"The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	fs.StringVar(&listenAddress, "address", "127.0.0.1:3456", "The address it will listen")
	fs.StringVar(&profileAddress, "pprofAddress", "127.0.0.1:3457", "The address for debug")
	fs.BoolVar(&nodeCacheCapable, "nodeCacheCapable", false, "The nodeCacheCapable enable node caching")
	fs.BoolVar(&enableComputeCapability, "enableComputeCapability", false, "Based on node device computing power scheduling: "+
		"Once the scheduler is turned on, it will try its best to schedule to node devices with high computing power levels")
}

func wordSepNormalizeFunc(f *pflag.FlagSet, name string) pflag.NormalizedName {
	if strings.Contains(name, "_") {
		return pflag.NormalizedName(strings.Replace(name, "_", "-", -1))
	}
	return pflag.NormalizedName(name)
}

// InitFlags normalizes and parses the command line flags
func initFlags() {
	pflag.CommandLine.SetNormalizeFunc(wordSepNormalizeFunc)
	// Only glog flags will be added
	flag.CommandLine.VisitAll(func(goflag *flag.Flag) {
		switch goflag.Name {
		case "logtostderr", "alsologtostderr",
			"v", "stderrthreshold", "vmodule", "log_backtrace_at", "log_dir":
			pflag.CommandLine.AddGoFlag(goflag)
		}
	})

	pflag.Parse()
}
