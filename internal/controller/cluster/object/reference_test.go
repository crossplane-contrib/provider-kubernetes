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

package object

import (
	"context"
	"encoding/json"
	"testing"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	v1alpha2 "github.com/crossplane-contrib/provider-kubernetes/apis/cluster/object/v1alpha2"
	"github.com/crossplane-contrib/provider-kubernetes/internal/controller/cluster/object/fake"
)

func TestObserveReferenceOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		manifest    string
		policies    xpv2.ManagementPolicies
		wantDesired bool
	}{
		{name: "Identity", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"}}`},
		{name: "Namespace", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live","namespace":"default"}}`},
		{name: "DefaultName", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"namespace":"default"}}`},
		{name: "DesiredData", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"},"data":{"key":"desired"}}`, wantDesired: true},
		{name: "DesiredLabel", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live","labels":{"app":"desired"}}}`, wantDesired: true},
		{name: "DesiredAnnotation", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live","annotations":{"app":"desired"}}}`, wantDesired: true},
		{name: "EmptySpec", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"},"spec":{}}`, wantDesired: true},
		{name: "NullSpec", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"},"spec":null}`, wantDesired: true},
		{name: "Create", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"}}`, policies: xpv2.ManagementPolicies{xpv2.ManagementActionObserve, xpv2.ManagementActionCreate}, wantDesired: true},
		{name: "Update", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"}}`, policies: xpv2.ManagementPolicies{xpv2.ManagementActionObserve, xpv2.ManagementActionUpdate}, wantDesired: true},
		{name: "All", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"}}`, policies: xpv2.ManagementPolicies{xpv2.ManagementActionAll}, wantDesired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := kubernetesObject(func(o *v1alpha2.Object) {
				o.Spec.ForProvider.Manifest.Raw = []byte(tc.manifest)
				o.Spec.ManagementPolicies = tc.policies
				if tc.policies == nil {
					o.Spec.ManagementPolicies = xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
				}
			})
			live := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "live"}, "data": map[string]interface{}{"key": "observed"},
			}}
			desiredCalled := false
			denied := kerrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "live", nil)
			e := &external{
				logger: logging.NewNopLogger(),
				client: resource.ClientApplicator{Client: &test.MockClient{MockGet: test.NewMockGetFn(nil, func(o client.Object) error { *o.(*unstructured.Unstructured) = *live.DeepCopy(); return nil })}},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(context.Context, *v1alpha2.Object, *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return live, nil
					},
					GetDesiredStateFn: func(context.Context, *v1alpha2.Object, *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						desiredCalled = true
						return nil, denied
					},
				},
			}
			observation, err := e.Observe(context.Background(), obj)
			if desiredCalled != tc.wantDesired {
				t.Fatalf("GetDesiredState called=%v, want %v", desiredCalled, tc.wantDesired)
			}
			if tc.wantDesired {
				if !kerrors.IsForbidden(err) {
					t.Fatalf("expected dry-run authorization error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("identity-only observation with read-only credentials failed: %v", err)
			}
			if !observation.ResourceExists || !observation.ResourceUpToDate {
				t.Fatalf("unexpected observation: %+v", observation)
			}
			observed := &unstructured.Unstructured{}
			if err := json.Unmarshal(obj.Status.AtProvider.Manifest.Raw, observed); err != nil {
				t.Fatal(err)
			}
			if observed.Object["data"].(map[string]interface{})["key"] != "observed" {
				t.Fatal("live resource was not published to atProvider")
			}
		})
	}
}
