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

	"github.com/google/go-cmp/cmp"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	v1alpha2 "github.com/crossplane-contrib/provider-kubernetes/apis/cluster/object/v1alpha2"
	"github.com/crossplane-contrib/provider-kubernetes/internal/controller/cluster/object/fake"
)

func TestObserveReferenceOnly(t *testing.T) {
	endpointSlice := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "discovery.k8s.io/v1",
		"kind":       "EndpointSlice",
		"metadata": map[string]interface{}{
			"name": "backend-abc", "namespace": "workload",
			"labels": map[string]interface{}{"kubernetes.io/service-name": "backend"},
		},
		"addressType": "IPv4",
		"endpoints": []interface{}{map[string]interface{}{
			"addresses":  []interface{}{"192.0.2.10"},
			"conditions": map[string]interface{}{"ready": true},
		}},
		"ports": []interface{}{map[string]interface{}{"name": "http", "protocol": "TCP", "port": int64(8080)}},
	}}
	for _, tc := range []struct {
		name        string
		manifest    string
		policies    xpv2.ManagementPolicies
		live        *unstructured.Unstructured
		labels      map[string]string
		annotations map[string]string
		references  []v1alpha2.Reference
		wantDesired bool
	}{
		{name: "Identity", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"}}`},
		{name: "Namespace", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live","namespace":"default"}}`},
		{name: "DefaultName", manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"namespace":"default"}}`},
		{
			name:     "SelectedEndpointSlice",
			manifest: `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice","metadata":{"name":"backend-abc","namespace":"workload"}}`,
			live:     endpointSlice,
		},
		{
			name:        "EndpointSliceCollectionMetadata",
			manifest:    `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice","metadata":{"name":"backend-abc","namespace":"workload"}}`,
			live:        endpointSlice,
			labels:      map[string]string{"kubernetes.crossplane.io/owned-by-collection": "backend-slices", "test.example/team": "networking"},
			annotations: map[string]string{"test.example/purpose": "endpoint-discovery"},
		},
		{
			name:        "ReferenceAddsDesiredData",
			manifest:    `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"live"}}`,
			references:  []v1alpha2.Reference{labelReference()},
			wantDesired: true,
		},
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
				o.SetName("live")
				o.Spec.ForProvider.Manifest.Raw = []byte(tc.manifest)
				o.Spec.References = tc.references
				o.SetLabels(tc.labels)
				o.SetAnnotations(tc.annotations)
				o.Spec.ManagementPolicies = tc.policies
				if tc.policies == nil {
					o.Spec.ManagementPolicies = xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
				}
			})
			live := tc.live
			if live == nil {
				live = &unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "live"}, "data": map[string]interface{}{"key": "observed"},
				}}
			}
			kube := &test.MockClient{
				MockGet: func(ctx context.Context, key client.ObjectKey, o client.Object) error {
					switch key.Name {
					case testReferenceObjectName:
						*o.(*unstructured.Unstructured) = *referenceObject()
					case live.GetName():
						*o.(*unstructured.Unstructured) = *live.DeepCopy()
					default:
						return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
					}
					return nil
				},
			}
			desiredCalled := false
			denied := kerrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "live", nil)
			e := &external{
				logger:      logging.NewNopLogger(),
				client:      resource.ClientApplicator{Client: kube},
				localClient: kube,
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(context.Context, *v1alpha2.Object, *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return live, nil
					},
					GetDesiredStateFn: func(ctx context.Context, o *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						desiredCalled = true
						if len(tc.references) > 0 {
							app, _, err := unstructured.NestedString(manifest.Object, "data", "app")
							if err != nil {
								t.Fatal(err)
							}
							if diff := cmp.Diff("foo", app); diff != "" {
								t.Errorf("GetDesiredState(...): -want resolved reference, +got:\n%s", diff)
							}
						}
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
			want := managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true, ConnectionDetails: managed.ConnectionDetails{}}
			if diff := cmp.Diff(want, observation); diff != "" {
				t.Errorf("Observe(...): -want observation, +got:\n%s", diff)
			}
			observed := &unstructured.Unstructured{}
			if err := json.Unmarshal(obj.Status.AtProvider.Manifest.Raw, observed); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(live, observed); diff != "" {
				t.Errorf("Observe(...): -want observed manifest, +got:\n%s", diff)
			}
		})
	}
}
