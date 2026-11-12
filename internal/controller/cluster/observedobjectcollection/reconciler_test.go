/*
Copyright 2024 The Crossplane Authors.

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

package observedobjectcollection

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-kubernetes/apis/cluster/object/v1alpha2"
	"github.com/crossplane-contrib/provider-kubernetes/apis/cluster/observedobjectcollection/v1alpha1"
	apisv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/cluster/v1alpha1"
	kubeclient "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

func TestReconciler(t *testing.T) {
	collectionName := types.NamespacedName{Name: "col"}
	errBoom := fmt.Errorf("error reading")
	pollIterval := 10 * time.Second
	objectAPIVersion := "v1"
	objectKind := "Foo"
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
	type args struct {
		client *test.MockClient
	}
	type want struct {
		r       reconcile.Result
		err     error
		patches *int
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ErrorGetCollection": {
			reason: "We should return error.",
			args: args{
				client: &test.MockClient{
					MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
						return errBoom
					},
				},
			},
			want: want{
				err: errBoom,
			},
		},
		"CollectionNotFound": {
			reason: "We should not return an error if the collection resource was not found.",
			args: args{
				client: &test.MockClient{
					MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
						return kerrors.NewNotFound(schema.GroupResource{}, "")
					},
				},
			},
		},
		"CreateObservedObjects": {
			reason: "Create observed-only object from the matched objects.",
			args: args{
				client: &test.MockClient{
					MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
						if _, ok := obj.(*apisv1alpha1.ProviderConfig); ok {
							return nil
						}

						if key != collectionName {
							return fmt.Errorf("Expected %v, but got %v", collectionName, key)
						}
						c := obj.(*v1alpha1.ObservedObjectCollection)
						c.Spec = v1alpha1.ObservedObjectCollectionSpec{
							ObserveObjects: v1alpha1.ObserveObjectCriteria{
								APIVersion: objectAPIVersion,
								Kind:       objectKind,
								Selector: metav1.LabelSelector{
									MatchLabels: map[string]string{
										"foo": "bar",
									},
								},
							},
						}
						c.Name = collectionName.Name
						return nil
					},
					MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						if olist, ok := list.(*v1alpha2.ObjectList); ok {
							olist.Items = append(olist.Items, v1alpha2.Object{ObjectMeta: metav1.ObjectMeta{Name: "col-foo0"}}, v1alpha2.Object{ObjectMeta: metav1.ObjectMeta{Name: "col-foo1"}})
							return nil
						}
						ulist := list.(*unstructured.UnstructuredList)
						if ulist.GetAPIVersion() != "v1" || ulist.GetKind() != "Foo" {
							return fmt.Errorf("Unexpected GVK %v", ulist.GroupVersionKind())
						}
						for i := 0; i < 2; i++ {
							item := unstructured.Unstructured{}
							item.SetKind(ulist.GetKind())
							item.SetAPIVersion(ulist.GetAPIVersion())
							item.SetName(fmt.Sprintf("foo%d", i))
							item.SetUID(types.UID(uuid.New().String()))
							ulist.Items = append(ulist.Items, item)
						}
						return nil
					},
					MockPatch: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if patch != client.Apply { //nolint:staticcheck // SA1019: match the production Patch call site's use of client.Apply
							return fmt.Errorf("Expected SSA patch, but got: %v", patch)
						}
						u := obj.(*unstructured.Unstructured)
						if u.GroupVersionKind() != v1alpha2.ObjectGroupVersionKind {
							return fmt.Errorf("Expected gvk %v, but got %v", v1alpha2.ObjectGroupVersionKind, u.GroupVersionKind())
						}
						if l := u.GetLabels()[membershipLabelKey]; l != collectionName.Name {
							return fmt.Errorf("Expecting membership label %v but got %v", collectionName.Name, l)
						}
						if s := sets.New[string]("col-foo0", "col-foo1"); !s.Has(u.GetName()) {
							return fmt.Errorf("Expecting one of %v, but got %v", s.UnsortedList(), u.GetName())
						}
						// Regression test for https://github.com/crossplane-contrib/provider-kubernetes/issues/555:
						// deletionPropagationPolicy should not be set by ObservedObjectCollection
						if p, found, _ := unstructured.NestedString(u.Object, "spec", "forProvider", "deletionPropagationPolicy"); found {
							return fmt.Errorf("Expected deletionPropagationPolicy to be nil, but got: %q", p)
						}
						manifest, found, err := unstructured.NestedMap(u.Object, "spec", "forProvider", "manifest")
						if err != nil {
							return err
						}
						if !found {
							return fmt.Errorf("Manifest not found")
						}
						if apiVersion := manifest["apiVersion"]; apiVersion != objectAPIVersion {
							return fmt.Errorf("Manifest apiVersion should be %v, but got: %v", objectAPIVersion, apiVersion)
						}
						if kind := manifest["kind"]; kind != objectKind {
							return fmt.Errorf("Manifest kind should be %v, but got: %v", objectKind, kind)
						}
						manifestMetadataName, _, _ := unstructured.NestedString(manifest, "metadata", "name")
						if s := sets.New[string]("foo0", "foo1"); !s.Has(manifestMetadataName) {
							return fmt.Errorf("Expecting one of %v, but got %v", s.UnsortedList(), manifestMetadataName)
						}
						return nil
					},
					MockStatusUpdate: func(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						c := obj.(*v1alpha1.ObservedObjectCollection)
						if cnd := c.Status.GetCondition(xpv2.TypeSynced); cnd.Status != corev1.ConditionTrue {
							panic(fmt.Sprintf("Object sync condition not true: %v", cnd.Message))
						}
						if cnd := c.Status.GetCondition(xpv2.TypeReady); cnd.Status != corev1.ConditionTrue {
							panic(fmt.Sprintf("Object ready condition not true: %v", cnd.Message))
						}
						if v := c.Status.MembershipLabel[membershipLabelKey]; v != collectionName.Name {
							panic(fmt.Sprintf("Expected membership label %v but got %v", collectionName.Name, v))
						}
						return nil
					},
				},
			},
			want: want{
				r: reconcile.Result{RequeueAfter: pollIterval},
			},
		},

		"CreateObservedEndpointSliceReference": {
			reason: "Label-selected EndpointSlices should create identity-only children without copying desired labels or endpoint data.",
			args: args{client: &test.MockClient{
				MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
					if _, ok := obj.(*apisv1alpha1.ProviderConfig); ok {
						return nil
					}
					if key != collectionName {
						return fmt.Errorf("Expected %v, but got %v", collectionName, key)
					}
					c := obj.(*v1alpha1.ObservedObjectCollection)
					c.Name, c.Namespace = collectionName.Name, collectionName.Namespace
					c.Spec = v1alpha1.ObservedObjectCollectionSpec{
						ProviderConfigReference: xpv2.Reference{Name: "readonly"},
						ObserveObjects: v1alpha1.ObserveObjectCriteria{
							APIVersion: endpointSlice.GetAPIVersion(), Kind: endpointSlice.GetKind(), Namespace: endpointSlice.GetNamespace(),
							Selector: metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/service-name": "backend"}},
						},
						Template: &v1alpha1.ObservedObjectTemplate{Metadata: v1alpha1.ObservedObjectTemplateMetadata{
							Labels:      map[string]string{"test.example/team": "networking"},
							Annotations: map[string]string{"test.example/purpose": "endpoint-discovery"},
						}},
					}
					return nil
				},
				MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*v1alpha2.ObjectList); ok {
						return nil
					}
					options := (&client.ListOptions{}).ApplyOptions(opts)
					if options.Namespace != "workload" || options.LabelSelector == nil || options.LabelSelector.String() != "kubernetes.io/service-name=backend" {
						return fmt.Errorf("Unexpected EndpointSlice selector: %+v", options)
					}
					items := list.(*unstructured.UnstructuredList)
					if items.GroupVersionKind() != endpointSlice.GroupVersionKind() {
						return fmt.Errorf("Unexpected list GVK: %v", items.GroupVersionKind())
					}
					items.Items = []unstructured.Unstructured{*endpointSlice.DeepCopy()}
					return nil
				},
				MockPatch: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					u := obj.(*unstructured.Unstructured)
					manifest, found, err := unstructured.NestedMap(u.Object, "spec", "forProvider", "manifest")
					if err != nil {
						return err
					}
					if !found {
						return fmt.Errorf("Manifest not found")
					}
					want := map[string]interface{}{
						"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
						"metadata": map[string]interface{}{"name": "backend-abc", "namespace": "workload"},
					}
					if diff := cmp.Diff(want, manifest); diff != "" {
						return fmt.Errorf("Expected identity-only child manifest (-want,+got): %s", diff)
					}
					policies, _, err := unstructured.NestedStringSlice(u.Object, "spec", "managementPolicies")
					if err != nil {
						return err
					}
					if diff := cmp.Diff([]string{"Observe"}, policies); diff != "" {
						return fmt.Errorf("Unexpected management policies: %s", diff)
					}
					if l := u.GetLabels()[membershipLabelKey]; l != collectionName.Name {
						return fmt.Errorf("Expecting membership label %v but got %v", collectionName.Name, l)
					}
					if l := u.GetLabels()["test.example/team"]; l != "networking" {
						return fmt.Errorf("Expecting template label test.example/team %q but got %q", "networking", l)
					}
					if a := u.GetAnnotations()["test.example/purpose"]; a != "endpoint-discovery" {
						return fmt.Errorf("Expecting template annotation test.example/purpose %q but got %q", "endpoint-discovery", a)
					}
					return nil
				},
				MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
			}},
			want: want{r: reconcile.Result{RequeueAfter: pollIterval}, patches: ptr.To(1)},
		},

		"RemoveNotMatchedObservedObjects": {
			reason: "Remove observe-only objects that either not exist or are not matched anymore",
			args: args{
				client: &test.MockClient{
					MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
						if _, ok := obj.(*apisv1alpha1.ProviderConfig); ok {
							return nil
						}

						if key != collectionName {
							return fmt.Errorf("Expected %v, but got %v", collectionName, key)
						}
						c := obj.(*v1alpha1.ObservedObjectCollection)
						c.Spec = v1alpha1.ObservedObjectCollectionSpec{
							ObserveObjects: v1alpha1.ObserveObjectCriteria{
								APIVersion: objectAPIVersion,
								Kind:       objectKind,
								Selector: metav1.LabelSelector{
									MatchLabels: map[string]string{
										"foo": "bar",
									},
								},
							},
						}
						c.Name = collectionName.Name
						return nil
					},
					MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						if olist, ok := list.(*v1alpha2.ObjectList); ok {
							olist.Items = append(olist.Items, v1alpha2.Object{ObjectMeta: metav1.ObjectMeta{Name: "col-foo0"}}, v1alpha2.Object{ObjectMeta: metav1.ObjectMeta{Name: "col-foo1"}}, v1alpha2.Object{ObjectMeta: metav1.ObjectMeta{Name: "col-foo2"}})
							return nil
						}
						ulist := list.(*unstructured.UnstructuredList)
						if ulist.GetAPIVersion() != "v1" || ulist.GetKind() != "Foo" {
							return fmt.Errorf("Unexpected GVK %v", ulist.GroupVersionKind())
						}
						for i := 0; i < 2; i++ {
							item := unstructured.Unstructured{}
							item.SetKind(ulist.GetKind())
							item.SetAPIVersion(ulist.GetAPIVersion())
							item.SetName(fmt.Sprintf("foo%d", i))
							item.SetUID(types.UID(uuid.New().String()))
							ulist.Items = append(ulist.Items, item)
						}
						return nil
					},
					MockDelete: func(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
						if obj.GetName() != "col-foo2" {
							return fmt.Errorf("Expected to remove col-foo2, but got %v", obj.GetName())
						}
						return nil
					},
					MockPatch: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if patch != client.Apply { //nolint:staticcheck // SA1019: match the production Patch call site's use of client.Apply
							return fmt.Errorf("Expected SSA patch, but got: %v", patch)
						}
						u := obj.(*unstructured.Unstructured)
						if u.GroupVersionKind() != v1alpha2.ObjectGroupVersionKind {
							return fmt.Errorf("Expected gvk %v, but got %v", v1alpha2.ObjectGroupVersionKind, u.GroupVersionKind())
						}
						if s := sets.New[string]("col-foo0", "col-foo1"); !s.Has(u.GetName()) {
							return fmt.Errorf("Expecting one of %v, but got %v", s.UnsortedList(), u.GetName())
						}
						// Regression test for https://github.com/crossplane-contrib/provider-kubernetes/issues/555:
						// deletionPropagationPolicy should not be set by ObservedObjectCollection
						if p, found, _ := unstructured.NestedString(u.Object, "spec", "forProvider", "deletionPropagationPolicy"); found {
							return fmt.Errorf("Expected deletionPropagationPolicy to be nil, but got: %q", p)
						}
						manifest, found, err := unstructured.NestedMap(u.Object, "spec", "forProvider", "manifest")
						if err != nil {
							return err
						}
						if !found {
							return fmt.Errorf("Manifest not found")
						}
						if apiVersion := manifest["apiVersion"]; apiVersion != objectAPIVersion {
							return fmt.Errorf("Manifest apiVersion should be %v, but got: %v", objectAPIVersion, apiVersion)
						}
						if kind := manifest["kind"]; kind != objectKind {
							return fmt.Errorf("Manifest kind should be %v, but got: %v", objectKind, kind)
						}
						manifestMetadataName, _, _ := unstructured.NestedString(manifest, "metadata", "name")
						if s := sets.New[string]("foo0", "foo1"); !s.Has(manifestMetadataName) {
							return fmt.Errorf("Expecting one of %v, but got %v", s.UnsortedList(), manifestMetadataName)
						}
						return nil
					},
					MockStatusUpdate: func(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						c := obj.(*v1alpha1.ObservedObjectCollection)
						if cnd := c.Status.GetCondition(xpv2.TypeSynced); cnd.Status != corev1.ConditionTrue {
							return fmt.Errorf("Object sync condition not true: %v", cnd.Message)
						}
						if cnd := c.Status.GetCondition(xpv2.TypeReady); cnd.Status != corev1.ConditionTrue {
							return fmt.Errorf("Object ready condition not true: %v", cnd.Message)
						}
						if v := c.Status.MembershipLabel[membershipLabelKey]; v != collectionName.Name {
							return fmt.Errorf("Expected membership label %v but got %v", collectionName.Name, v)
						}
						return nil
					},
				},
			},
			want: want{
				r: reconcile.Result{RequeueAfter: pollIterval},
			},
		},
		"ErrorListingObjects": {
			reason: "Return error and set collection status if listing objects produces error.",
			args: args{
				client: &test.MockClient{
					MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
						if _, ok := obj.(*apisv1alpha1.ProviderConfig); ok {
							return nil
						}

						if key != collectionName {
							return fmt.Errorf("Expected %v, but got %v", collectionName, key)
						}
						c := obj.(*v1alpha1.ObservedObjectCollection)
						c.Spec = v1alpha1.ObservedObjectCollectionSpec{
							ObserveObjects: v1alpha1.ObserveObjectCriteria{
								APIVersion: objectAPIVersion,
								Kind:       objectKind,
								Selector: metav1.LabelSelector{
									MatchLabels: map[string]string{
										"foo": "bar",
									},
								},
							},
						}
						c.Name = collectionName.Name
						return nil
					},
					MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						return errBoom
					},
					MockStatusUpdate: func(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						c := obj.(*v1alpha1.ObservedObjectCollection)
						if cnd := c.Status.GetCondition(xpv2.TypeSynced); cnd.Status != corev1.ConditionFalse {
							panic(fmt.Sprintf("Object sync condition not true: %v", cnd.Message))
						}

						return nil
					},
				},
			},
			want: want{
				err: errBoom,
			},
		},
		"ErrorCreatingObservedObjects": {
			reason: "Return error and update collection status if error occurs while creating observe only object",
			args: args{
				client: &test.MockClient{
					MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
						if _, ok := obj.(*apisv1alpha1.ProviderConfig); ok {
							return nil
						}

						if key != collectionName {
							return fmt.Errorf("Expected %v, but got %v", collectionName, key)
						}
						c := obj.(*v1alpha1.ObservedObjectCollection)
						c.Spec = v1alpha1.ObservedObjectCollectionSpec{
							ObserveObjects: v1alpha1.ObserveObjectCriteria{
								APIVersion: objectAPIVersion,
								Kind:       objectKind,
								Selector: metav1.LabelSelector{
									MatchLabels: map[string]string{
										"foo": "bar",
									},
								},
							},
						}
						c.Name = collectionName.Name
						return nil
					},
					MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						if olist, ok := list.(*v1alpha2.ObjectList); ok {
							olist.Items = append(olist.Items, v1alpha2.Object{ObjectMeta: metav1.ObjectMeta{Name: "col-foo0"}}, v1alpha2.Object{ObjectMeta: metav1.ObjectMeta{Name: "col-foo1"}})
							return nil
						}
						ulist := list.(*unstructured.UnstructuredList)
						if ulist.GetAPIVersion() != "v1" || ulist.GetKind() != "Foo" {
							return fmt.Errorf("Unexpected GVK %v", ulist.GroupVersionKind())
						}
						for i := 0; i < 2; i++ {
							item := unstructured.Unstructured{}
							item.SetKind(ulist.GetKind())
							item.SetAPIVersion(ulist.GetAPIVersion())
							item.SetName(fmt.Sprintf("foo%d", i))
							item.SetUID(types.UID(uuid.New().String()))
							ulist.Items = append(ulist.Items, item)
						}
						return nil
					},
					MockPatch: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						return errBoom
					},
					MockStatusUpdate: func(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						c := obj.(*v1alpha1.ObservedObjectCollection)
						if cnd := c.Status.GetCondition(xpv2.TypeSynced); cnd.Status != corev1.ConditionFalse {
							panic(fmt.Sprintf("Object sync condition not true: %v", cnd.Message))
						}
						return nil
					},
				},
			},
			want: want{
				err: errBoom,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			patches := 0
			if tc.want.patches != nil {
				patch := tc.args.client.MockPatch
				tc.args.client.MockPatch = func(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
					patches++
					return patch(ctx, obj, p, opts...)
				}
			}
			r := &Reconciler{
				client: tc.args.client,
				log:    logging.NewNopLogger(),
				clientBuilder: kubeclient.BuilderFn(func(ctx context.Context, pc kconfig.ProviderConfigSpec) (client.Client, *rest.Config, error) {
					return tc.args.client, nil, nil
				}),
				observedObjectName: func(collection client.Object, matchedObject client.Object) (string, error) {
					return fmt.Sprintf("%s-%s", collection.GetName(), matchedObject.GetName()), nil
				},
				pollInterval: func() time.Duration {
					return pollIterval
				},
			}
			got, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: collectionName})
			if tc.want.patches != nil {
				if diff := cmp.Diff(*tc.want.patches, patches); diff != "" {
					t.Errorf("\n%s\nr.Reconcile(...): -want patches, +got patches:\n%s", tc.reason, diff)
				}
			}
			if !errors.Is(err, tc.want.err) {
				t.Errorf("\n%s\nr.Reconcile(...): want error: %v, got error: %v", tc.reason, tc.want.err, err)
			}
			if diff := cmp.Diff(tc.want.r, got, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nr.Reconcile(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
