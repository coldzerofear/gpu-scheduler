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
package route

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"k8s.io/klog"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"

	"tkestack.io/gpu-admission/pkg/predicate"
	"tkestack.io/gpu-admission/pkg/version"
)

const (
	// version router path
	versionPath = "/version"
	apiPrefix   = "/scheduler"
	// predication router path
	filterPerfix = apiPrefix + "/filter"
	bindPerfix   = apiPrefix + "/bind"
)

func checkBody(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		http.Error(w, "Please send a request body", 400)
		return
	}
}

// PredicateRoute sets router table for predication
func FilterPredicateRoute(predicate predicate.FilterPredicate, nodeCacheCapable bool) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		checkBody(w, r)

		var buf bytes.Buffer
		body := io.TeeReader(r.Body, &buf)

		var extenderArgs extenderv1.ExtenderArgs
		var extenderFilterResult *extenderv1.ExtenderFilterResult

		if err := json.NewDecoder(body).Decode(&extenderArgs); err != nil {
			klog.Errorf("Decode extender filter args err, %v", err)
			extenderFilterResult = &extenderv1.ExtenderFilterResult{
				Nodes:       nil,
				FailedNodes: nil,
				Error:       err.Error(),
			}
		} else {
			if nodeCacheCapable {
				extenderFilterResult = predicate.FilterOnCache(extenderArgs)
				klog.V(2).Info("nodeCacheCapable enable")
			} else {
				extenderFilterResult = predicate.Filter(extenderArgs)
				klog.V(2).Info("nodeCacheCapable disable")
			}
			klog.V(4).Infof("%s: ExtenderArgs = %+v", predicate.Name(), extenderArgs)
		}

		if resultBody, err := json.Marshal(extenderFilterResult); err != nil {
			klog.Errorf("Failed to marshal extenderFilterResult: %+v, %+v",
				err, extenderFilterResult)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
		} else {
			klog.V(4).Infof("%s: extenderFilterResult = %s",
				predicate.Name(), string(resultBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(resultBody)
		}
	}
}

// VersionRoute returns the version of router in response
func VersionRoute(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	fmt.Fprint(w, fmt.Sprint(version.Get()))
}

func AddVersion(router *httprouter.Router) {
	router.GET(versionPath, DebugLogging(VersionRoute, versionPath))
}

// DebugLogging wraps handler for debugging purposes
func DebugLogging(h httprouter.Handle, path string) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		klog.V(10).Infof("%s request body = %s", path, r.Body)
		h(w, r, p)
		klog.V(10).Infof("%s response = %s", path, w)
	}
}

func AddFilterPredicate(router *httprouter.Router, predicate predicate.FilterPredicate, nodeCacheCapable bool) {
	path := filterPerfix
	router.POST(path, DebugLogging(FilterPredicateRoute(predicate, nodeCacheCapable), path))
}

func AddBindPredicate(router *httprouter.Router, predicate predicate.BindPredicate) {
	path := bindPerfix
	router.POST(path, DebugLogging(BindPredicateRoute(predicate), path))
}

func BindPredicateRoute(predicate predicate.BindPredicate) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		checkBody(w, r)

		var buf bytes.Buffer
		body := io.TeeReader(r.Body, &buf)

		var extenderBindingArgs extenderv1.ExtenderBindingArgs
		var extenderBindingResult *extenderv1.ExtenderBindingResult

		if err := json.NewDecoder(body).Decode(&extenderBindingArgs); err != nil {
			klog.Errorf("Decode extender binding args err, %v", err)
			extenderBindingResult = &extenderv1.ExtenderBindingResult{
				Error: err.Error(),
			}
		} else {
			extenderBindingResult = predicate.Bind(extenderBindingArgs)
			klog.V(4).Infof("%s: ExtenderBindingArgs = %+v", predicate.Name(), extenderBindingArgs)
		}

		if resultBody, err := json.Marshal(extenderBindingResult); err != nil {
			klog.Errorf("Failed to marshal extenderBindingResult: %+v, %+v",
				err, extenderBindingResult)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			errMsg := fmt.Sprintf("{'error':'%s'}", err.Error())
			w.Write([]byte(errMsg))
		} else {
			klog.V(4).Infof("%s: extenderBindingResult = %s",
				predicate.Name(), string(resultBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(resultBody)
		}
	}
}
