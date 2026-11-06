/*
Copyright 2026 The Crossplane Authors.

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

package controller

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// IsIdentityOnly reports whether a manifest contains only resource identity.
// Any additional field, including an empty or null one, may express desired
// state and must retain the normal comparison path.
func IsIdentityOnly(manifest *unstructured.Unstructured) bool {
	if manifest == nil {
		return false
	}
	for key := range manifest.Object {
		switch key {
		case "apiVersion", "kind", "metadata":
		default:
			return false
		}
	}
	metadata, ok := manifest.Object["metadata"].(map[string]interface{})
	if !ok {
		return false
	}
	for key := range metadata {
		if key != "name" && key != "namespace" {
			return false
		}
	}
	return true
}
