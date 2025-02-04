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
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
)

type FilterPredicate interface {
	// Name returns the name of this predictor
	Name() string
	// Filter returns the filter result of predictor, this will tell the suitable nodes to running
	// pod
	Filter(args extenderv1.ExtenderArgs) *extenderv1.ExtenderFilterResult
	// cache模式
	FilterOnCache(args extenderv1.ExtenderArgs) *extenderv1.ExtenderFilterResult
}

type BindPredicate interface {
	// Name returns the name of this predictor
	Name() string
	// Pod绑定节点接口
	Bind(args extenderv1.ExtenderBindingArgs) *extenderv1.ExtenderBindingResult
}
