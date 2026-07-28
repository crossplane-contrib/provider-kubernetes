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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestUpToDate(t *testing.T) {
	cm := func(data map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"data":       data,
		}}
	}

	type args struct {
		last    *unstructured.Unstructured
		desired *unstructured.Unstructured
	}
	type want struct {
		isUpToDate  bool
		diffIsEmpty bool
		// diffContains, when set, must appear in the returned diff.
		diffContains string
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"InSync": {
			args: args{
				last:    cm(map[string]interface{}{"k": "v"}),
				desired: cm(map[string]interface{}{"k": "v"}),
			},
			want: want{isUpToDate: true, diffIsEmpty: true},
		},
		"Drift": {
			args: args{
				last:    cm(map[string]interface{}{"k": "observed"}),
				desired: cm(map[string]interface{}{"k": "desired"}),
			},
			want: want{isUpToDate: false, diffIsEmpty: false, diffContains: "desired"},
		},
		"NilObserved": {
			args: args{
				last:    nil,
				desired: cm(map[string]interface{}{"k": "v"}),
			},
			want: want{isUpToDate: false, diffIsEmpty: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotUpToDate, gotDiff := UpToDate(tc.args.last, tc.args.desired)
			if diff := cmp.Diff(tc.want.isUpToDate, gotUpToDate); diff != "" {
				t.Errorf("UpToDate(...) isUpToDate: -want +got:\n%s", diff)
			}
			if gotIsEmpty := gotDiff == ""; gotIsEmpty != tc.want.diffIsEmpty {
				t.Errorf("UpToDate(...) diff emptiness: want empty=%v, got diff=%q", tc.want.diffIsEmpty, gotDiff)
			}
			if tc.want.diffContains != "" && !strings.Contains(gotDiff, tc.want.diffContains) {
				t.Errorf("UpToDate(...) diff: want contains %q, got:\n%s", tc.want.diffContains, gotDiff)
			}
		})
	}
}
