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
	"testing"

	"github.com/google/go-cmp/cmp"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

func TestSecretSourcedPath(t *testing.T) {
	type args struct {
		apiVersion  string
		kind        string
		fieldPath   *string
		toFieldPath *string
	}
	type want struct {
		path string
		ok   bool
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SecretToFieldPath": {
			reason: "A patch from a v1 Secret should write to its toFieldPath.",
			args: args{
				apiVersion:  "v1",
				kind:        "Secret",
				fieldPath:   ptr.To("data.password"),
				toFieldPath: ptr.To("spec.password"),
			},
			want: want{path: "spec.password", ok: true},
		},
		"SecretDefaultsToFieldPath": {
			reason: "A patch from a v1 Secret without a toFieldPath should write to its fieldPath.",
			args: args{
				apiVersion: "v1",
				kind:       "Secret",
				fieldPath:  ptr.To("data.password"),
			},
			want: want{path: "data.password", ok: true},
		},
		"NotASecret": {
			reason: "A patch from a ConfigMap is not Secret-sourced.",
			args: args{
				apiVersion: "v1",
				kind:       "ConfigMap",
				fieldPath:  ptr.To("data.password"),
			},
		},
		"NotACoreSecret": {
			reason: "Only the core v1 Secret is a Secret.",
			args: args{
				apiVersion: "example.org/v1",
				kind:       "Secret",
				fieldPath:  ptr.To("data.password"),
			},
		},
		"NoFieldPath": {
			reason: "A reference to a Secret without a fieldPath does not patch anything.",
			args: args{
				apiVersion: "v1",
				kind:       "Secret",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path, ok := SecretSourcedPath(tc.args.apiVersion, tc.args.kind, tc.args.fieldPath, tc.args.toFieldPath)
			if diff := cmp.Diff(tc.want.path, path); diff != "" {
				t.Errorf("%s\nSecretSourcedPath(...): -want path, +got path:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.ok, ok); diff != "" {
				t.Errorf("%s\nSecretSourcedPath(...): -want ok, +got ok:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestValidateSecretSourcedPath(t *testing.T) {
	cases := map[string]struct {
		reason string
		path   string
		want   error
	}{
		"DataKey": {
			reason: "A value read from a Secret may be patched into the data of a manifest.",
			path:   "data.password",
		},
		"Label": {
			reason: "A value read from a Secret may be patched into other metadata of a manifest.",
			path:   "metadata.labels.app",
		},
		"NestedName": {
			reason: "Only the name of the manifest itself identifies the object.",
			path:   "spec.template.metadata.name",
		},
		"Metadata": {
			reason: "A value read from a Secret must not replace the metadata of a manifest.",
			path:   "metadata",
			want:   errors.Errorf(errFmtSecretSourcedMetadata, "metadata"),
		},
		"Name": {
			reason: "A value read from a Secret must not set the name of a manifest.",
			path:   "metadata.name",
			want:   errors.Errorf(errFmtSecretSourcedMetadata, "metadata.name"),
		},
		"Namespace": {
			reason: "A value read from a Secret must not set the namespace of a manifest.",
			path:   "metadata[namespace]",
			want:   errors.Errorf(errFmtSecretSourcedMetadata, "metadata[namespace]"),
		},
		"WholeManifest": {
			reason: "A value read from a Secret must not replace the whole manifest.",
			path:   "",
			want:   errors.Errorf(errFmtSecretSourcedMetadata, ""),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateSecretSourcedPath(tc.path)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nValidateSecretSourcedPath(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestRedactSecretValues(t *testing.T) {
	marker := map[string]interface{}{"redacted": nil}
	lastApplied := map[string]interface{}{
		v1.LastAppliedConfigAnnotation: `{"data":{"password":"s3cr3t-value"}}`,
	}

	type args struct {
		u      map[string]interface{}
		paths  []string
		values []string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   map[string]interface{}
	}{
		"SecretValuesElsewhere": {
			reason: "Values read from a Secret should be redacted wherever else they are, e.g. at another list index after a mutating admission, but not in the fields that identify the object or in its managed fields.",
			args: args{
				u: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":          "s3cr3t-value",
						"annotations":   map[string]interface{}{"copy": "user:s3cr3t-value"},
						"managedFields": []interface{}{map[string]interface{}{"manager": "s3cr3t-value"}},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{"name": "sidecar"},
							map[string]interface{}{"name": "app", "env": []interface{}{map[string]interface{}{"value": "s3cr3t-value"}}},
						},
					},
				},
				paths:  []string{"spec.containers[0].env[0].value"},
				values: []string{"s3cr3t-value"},
			},
			want: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name":          "s3cr3t-value",
					"annotations":   map[string]interface{}{"copy": "user:<redacted>"},
					"managedFields": []interface{}{map[string]interface{}{"manager": "s3cr3t-value"}},
				},
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{"name": "sidecar"},
						map[string]interface{}{"name": "app", "env": []interface{}{map[string]interface{}{"value": "<redacted>"}}},
					},
				},
			},
		},
		"SecretSourcedPaths": {
			reason: "The values at paths patched from a Secret should be redacted, wherever they are, except the name, and nothing should be added.",
			args: args{
				u: map[string]interface{}{
					"apiVersion": "example.org/v1",
					"kind":       "Database",
					"metadata":   map[string]interface{}{"name": "db"},
					"spec": map[string]interface{}{
						"password": "s3cr3t-value",
						"users":    []interface{}{map[string]interface{}{"name": "admin", "password": "s3cr3t-value"}},
						"host":     "db.example.org",
					},
				},
				paths: []string{"spec.password", "spec.users[0].password", "spec.missing", "metadata.name"},
			},
			want: map[string]interface{}{
				"apiVersion": "example.org/v1",
				"kind":       "Database",
				"metadata":   map[string]interface{}{"name": "db"},
				"spec": map[string]interface{}{
					"password": "<redacted>",
					"users":    []interface{}{map[string]interface{}{"name": "admin", "password": "<redacted>"}},
					"host":     "db.example.org",
				},
			},
		},
		"SecretData": {
			reason: "The data and stringData of a Secret should be replaced with a redaction marker.",
			args: args{
				u: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Secret",
					"data":       map[string]interface{}{"password": "czNjcjN0LXZhbHVl"},
					"stringData": map[string]interface{}{"user": "admin"},
				},
			},
			want: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Secret",
				"data":       marker,
				"stringData": marker,
			},
		},
		"SecretWithoutData": {
			reason: "A Secret should get a redacted data field, as it always did in status.",
			args: args{
				u: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Secret",
				},
			},
			want: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Secret",
				"data":       marker,
			},
		},
		"SecretLastApplied": {
			reason: "The last applied configuration of a Secret holds its data, so it should be removed.",
			args: args{
				u: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Secret",
					"metadata": map[string]interface{}{
						"name":        "creds",
						"annotations": lastApplied,
					},
				},
			},
			want: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata":   map[string]interface{}{"name": "creds"},
				"data":       marker,
			},
		},
		"SecretSourcedPathLastApplied": {
			reason: "The last applied configuration holds the values patched from a Secret, so it should be removed, and the other annotations kept.",
			args: args{
				u: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"annotations": map[string]interface{}{
							v1.LastAppliedConfigAnnotation: `{"data":{"password":"s3cr3t-value"}}`,
							"team":                         "a",
						},
					},
					"data": map[string]interface{}{"password": "s3cr3t-value"},
				},
				paths: []string{"data.password"},
			},
			want: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{"team": "a"},
				},
				"data": map[string]interface{}{"password": "<redacted>"},
			},
		},
		"NothingToRedact": {
			reason: "An object that is not a Secret, without paths patched from a Secret, should be left as it is.",
			args: args{
				u: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"annotations": lastApplied,
					},
					"data": map[string]interface{}{"password": "s3cr3t-value"},
				},
			},
			want: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"annotations": lastApplied,
				},
				"data": map[string]interface{}{"password": "s3cr3t-value"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: tc.args.u}
			values := &SecretValues{}
			for _, v := range tc.args.values {
				values.add(v, false)
			}
			if err := RedactSecretValues(u, tc.args.paths, values); err != nil {
				t.Fatalf("RedactSecretValues(...): %v", err)
			}
			if diff := cmp.Diff(tc.want, u.Object); diff != "" {
				t.Errorf("%s\nRedactSecretValues(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestLoggableObserved(t *testing.T) {
	observed := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"data":       map[string]interface{}{"password": "s3cr3t-value"},
		}}
	}

	cases := map[string]struct {
		reason string
		paths  []string
		want   interface{}
	}{
		"Redacted": {
			reason: "The logged object should be redacted.",
			paths:  []string{"data.password"},
			want: &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"data":       map[string]interface{}{"password": "<redacted>"},
			}},
		},
		"NothingToRedact": {
			reason: "Without paths patched from a Secret, an object that is not a Secret should be logged as it is.",
			want:   observed(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u := observed()
			got := LoggableObserved(u, tc.paths, nil).MarshalLog()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nMarshalLog(): -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(observed(), u); diff != "" {
				t.Errorf("MarshalLog(): observed object was mutated: -want, +got:\n%s", diff)
			}
		})
	}
}

func TestLoggableIdentity(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "cm", "namespace": "default"},
		"data":       map[string]interface{}{"password": "s3cr3t-value"},
	}}
	want := map[string]string{"apiVersion": "v1", "kind": "ConfigMap", "namespace": "default", "name": "cm"}
	if diff := cmp.Diff(want, LoggableIdentity(u).MarshalLog()); diff != "" {
		t.Errorf("Only the identity of the observed object should be logged.\nMarshalLog(): -want, +got:\n%s", diff)
	}
}

func TestSecretValuesScrubError(t *testing.T) {
	secret := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{"token": "dG9rZW4tdmFsdWU="},
		},
		"data": map[string]interface{}{
			// s3cr3t-value
			"password": "czNjcjN0LXZhbHVl",
			// 1234
			"pin": "MTIzNA==",
			// -----BEGIN-----\n"a&b"
			"cert": "LS0tLS1CRUdJTi0tLS0tCiJhJmIi",
		},
	}}

	type args struct {
		fieldPaths []string
		err        error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   error
	}{
		"NoError": {
			reason: "No error should stay no error.",
			args: args{
				fieldPaths: []string{"data.password"},
			},
		},
		"DataValue": {
			reason: "A value of the data of a Secret should be scrubbed, base64 encoded and decoded.",
			args: args{
				fieldPaths: []string{"data.password"},
				err:        errors.New(`Invalid value: "czNjcjN0LXZhbHVl": s3cr3t-value is not allowed`),
			},
			want: errors.New(`Invalid value: "<redacted>": <redacted> is not allowed`),
		},
		"NotData": {
			reason: "A value outside the data of a Secret is not base64 encoded, so only it should be scrubbed.",
			args: args{
				fieldPaths: []string{"metadata.annotations.token"},
				err:        errors.New(`dG9rZW4tdmFsdWU= or token-value`),
			},
			want: errors.New(`<redacted> or token-value`),
		},
		"ShortValues": {
			reason: "Values shorter than 6 characters should not be scrubbed.",
			args: args{
				fieldPaths: []string{"data.pin"},
				err:        errors.New(`MTIzNA== is 1234`),
			},
			want: errors.New(`<redacted> is 1234`),
		},
		"QuotedValue": {
			reason: "A value should also be scrubbed in the escaped form it takes when quoted.",
			args: args{
				fieldPaths: []string{"data.cert"},
				err:        errors.Errorf(`Invalid value: %q`, "-----BEGIN-----\n\"a&b\""),
			},
			want: errors.New(`Invalid value: "<redacted>"`),
		},
		"JSONValue": {
			reason: "A value should also be scrubbed in the escaped form it takes in JSON.",
			args: args{
				fieldPaths: []string{"data.cert"},
				err:        errors.New(`Object 'Kind' is missing in '{"cert":"-----BEGIN-----\n\"a\u0026b\""}'`),
			},
			want: errors.New(`Object 'Kind' is missing in '{"cert":"<redacted>"}'`),
		},
		"AllValues": {
			reason: "Every string in a value read from a Secret should be scrubbed.",
			args: args{
				fieldPaths: []string{"data"},
				err:        errors.New(`czNjcjN0LXZhbHVl and s3cr3t-value and dG9rZW4tdmFsdWU=`),
			},
			want: errors.New(`<redacted> and <redacted> and dG9rZW4tdmFsdWU=`),
		},
		"NothingToScrub": {
			reason: "An error without recorded values should be returned as it is.",
			args: args{
				fieldPaths: []string{"data.missing"},
				err:        errors.Wrap(errors.New("boom"), "cannot apply object"),
			},
			want: errors.Wrap(errors.New("boom"), "cannot apply object"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var s SecretValues
			for _, p := range tc.args.fieldPaths {
				s.Add(secret, p)
			}
			got := s.ScrubError(tc.args.err)
			if diff := cmp.Diff(tc.want, got, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nScrubError(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}
