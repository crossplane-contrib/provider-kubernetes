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

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIsIdentityOnly(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"ClusterScoped", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live"}}`, true},
		{"Namespaced", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live","namespace":"default"}}`, true},
		{"Data", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"},"data":{"key":"value"}}`, false},
		{"EmptyData", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"},"data":{}}`, false},
		{"Labels", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live","labels":{"app":"desired"}}}`, false},
		{"EmptyLabels", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live","labels":{}}}`, false},
		{"Annotations", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live","annotations":{"key":"value"}}}`, false},
		{"Finalizers", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live","finalizers":["keep"]}}`, false},
		{"EmptySpec", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live"},"spec":{}}`, false},
		{"NullSpec", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live"},"spec":null}`, false},
		{"Status", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"live"},"status":{}}`, false},
		{"MissingMetadata", `{"apiVersion":"v1","kind":"Namespace"}`, false},
		{"NullMetadata", `{"apiVersion":"v1","kind":"Namespace","metadata":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &unstructured.Unstructured{}
			if err := json.Unmarshal([]byte(tc.raw), manifest); err != nil {
				t.Fatal(err)
			}
			before := manifest.DeepCopy()
			if got := IsIdentityOnly(manifest); got != tc.want {
				t.Errorf("IsIdentityOnly() = %v, want %v", got, tc.want)
			}
			if diff := cmp.Diff(before, manifest); diff != "" {
				t.Fatalf("manifest was mutated (-before,+after): %s", diff)
			}
		})
	}
	if IsIdentityOnly(nil) {
		t.Error("nil manifest must not be treated as a reference")
	}
}
