/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// +k8s:validation-gen=TypeMeta
// +k8s:validation-gen-scheme-registry=k8s.io/code-generator/cmd/validation-gen/testscheme.Scheme

// This is a test package.
package multiplekeys

import "k8s.io/code-generator/cmd/validation-gen/testscheme"

var localSchemeBuilder = testscheme.New()

type Struct struct {
	TypeMeta int `json:"typeMeta"`

	// +k8s:listType=map
	// +k8s:listMapKey=stringKey
	// +k8s:listMapKey=intKey
	// +k8s:listMapKey=boolKey
	// +k8s:item(stringKey: "target", intKey: 42, boolKey: true)=+k8s:validateFalse="item Items[stringKey=target,intKey=42,boolKey=true]"
	Items []Item `json:"items"`
}

type Item struct {
	StringKey string `json:"stringKey"`
	IntKey    int    `json:"intKey"`
	BoolKey   bool   `json:"boolKey"`
	Data      string `json:"data"`
}
