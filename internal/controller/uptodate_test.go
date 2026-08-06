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

func TestUpdateModeDiff(t *testing.T) {
	// cm builds a ConfigMap as it exists on the cluster, including mechanical
	// metadata and status that must never appear in the diff.
	cm := func(data, labels map[string]interface{}) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":              "cm",
				"namespace":         "default",
				"resourceVersion":   "12345",
				"uid":               "abc-123",
				"creationTimestamp": "2026-07-28T00:00:00Z",
				"generation":        int64(7),
				"managedFields": []interface{}{
					map[string]interface{}{"manager": "external-actor", "operation": "Apply"},
				},
			},
			"data":   data,
			"status": map[string]interface{}{"observedGeneration": int64(7)},
		}}
		if labels != nil {
			_ = unstructured.SetNestedMap(u.Object, labels, "metadata", "labels")
		}
		return u
	}
	secret := func(data map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"type":       "Opaque",
			"metadata":   map[string]interface{}{"name": "s", "namespace": "default"},
			"data":       data,
		}}
	}

	type args struct {
		current    *unstructured.Unstructured
		desiredObj *unstructured.Unstructured
	}
	type want struct {
		diffIsEmpty bool
		// contains are substrings expected anywhere in the diff.
		contains []string
		// absent are substrings that must not appear anywhere in the diff.
		absent []string
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"ManifestFieldWouldChange": {
			// The would-be update changes data.sample-key; other live fields
			// the manifest does not touch are carried through unchanged by the
			// SSA dry-run, so they must not appear in the diff. Mechanical
			// metadata and status must never appear.
			args: args{
				current: cm(map[string]interface{}{
					"sample-key":        "live-external-value",
					"external-only-key": "external-data",
				}, map[string]interface{}{"external-label": "keep-me"}),
				desiredObj: cm(map[string]interface{}{
					"sample-key":        "desired-value",
					"external-only-key": "external-data",
				}, map[string]interface{}{"external-label": "keep-me"}),
			},
			want: want{
				diffIsEmpty: false,
				contains:    []string{"~ data.sample-key: live-external-value -> desired-value"},
				absent: []string{
					"external-only-key", "external-label", // unchanged fields omitted entirely
					"managedFields", "resourceVersion", "creationTimestamp", "observedGeneration", "12345",
				},
			},
		},
		"MultipleFieldsChangeAcrossPaths": {
			// A change to a nested non-data field (metadata.labels) and an added
			// key are each reported by their full key path.
			args: args{
				current: cm(map[string]interface{}{"sample-key": "v"},
					map[string]interface{}{"env": "prod"}),
				desiredObj: cm(map[string]interface{}{"sample-key": "v"},
					map[string]interface{}{"env": "staging", "team": "infra"}),
			},
			want: want{
				diffIsEmpty: false,
				contains: []string{
					"~ metadata.labels.env: prod -> staging",
					"+ metadata.labels.team: infra",
				},
			},
		},
		"SecretValuesRedacted": {
			// For a v1 Secret, the changed data key is reported by path, but its
			// (base64) value must be redacted rather than leaked into the diff.
			args: args{
				current:    secret(map[string]interface{}{"password": "bGl2ZQ=="}),
				desiredObj: secret(map[string]interface{}{"password": "ZGVzaXJlZA=="}),
			},
			want: want{
				diffIsEmpty: false,
				contains:    []string{"~ data.password: <redacted> -> <redacted>"},
				absent:      []string{"bGl2ZQ==", "ZGVzaXJlZA=="},
			},
		},
		"SecretDataAddedWholesaleRedacted": {
			// The desired Secret adds data the live object lacks. The diff
			// reports the change at the data-map level (path ["data"]); the
			// values must still be redacted, not leaked wholesale.
			args: args{
				current: &unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
					"metadata": map[string]interface{}{"name": "s", "namespace": "default"},
				}},
				desiredObj: secret(map[string]interface{}{"password": "ZGVzaXJlZA=="}),
			},
			want: want{
				diffIsEmpty: false,
				contains:    []string{"data"},
				absent:      []string{"ZGVzaXJlZA=="},
			},
		},
		"SecretDataRemovedWholesaleRedacted": {
			// The desired Secret drops data the live object has. The removed
			// value must be redacted, not leaked.
			args: args{
				current: secret(map[string]interface{}{"password": "bGl2ZQ=="}),
				desiredObj: &unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
					"metadata": map[string]interface{}{"name": "s", "namespace": "default"},
				}},
			},
			want: want{
				diffIsEmpty: false,
				contains:    []string{"data"},
				absent:      []string{"bGl2ZQ=="},
			},
		},
		"OnlyMechanicalMetadataDiffers": {
			// Two objects that differ only in mechanical metadata / status are
			// considered to have no meaningful update-mode change.
			args: args{
				current: cm(map[string]interface{}{"sample-key": "v"}, nil),
				desiredObj: func() *unstructured.Unstructured {
					u := cm(map[string]interface{}{"sample-key": "v"}, nil)
					_ = unstructured.SetNestedField(u.Object, "99999", "metadata", "resourceVersion")
					return u
				}(),
			},
			want: want{diffIsEmpty: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := UpdateModeDiff(tc.args.current, tc.args.desiredObj)
			if gotIsEmpty := got == ""; gotIsEmpty != tc.want.diffIsEmpty {
				t.Fatalf("UpdateModeDiff(...): want empty=%v, got diff=%q", tc.want.diffIsEmpty, got)
			}
			for _, s := range tc.want.contains {
				if !strings.Contains(got, s) {
					t.Errorf("UpdateModeDiff(...): want diff to contain %q, got:\n%s", s, got)
				}
			}
			for _, s := range tc.want.absent {
				if strings.Contains(got, s) {
					t.Errorf("UpdateModeDiff(...): want diff NOT to contain %q, got:\n%s", s, got)
				}
			}
		})
	}
}
