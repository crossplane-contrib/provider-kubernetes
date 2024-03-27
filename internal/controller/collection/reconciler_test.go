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

package collection

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
	"github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

func TestReconciler(t *testing.T) {
	collectionName := types.NamespacedName{Name: "col"}
	errBoom := fmt.Errorf("error reading")
	pollIterval := 10 * time.Second
	objectAPIVersion := "v1"
	objectKind := "Foo"
	type args struct {
		client *test.MockClient
	}
	type want struct {
		r   reconcile.Result
		err error
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
						if key != collectionName {
							return fmt.Errorf("Expected %v, but got %v", collectionName, key)
						}
						c := obj.(*v1alpha1.ObservedObjectCollection)
						c.Spec = v1alpha1.ObservedObjectCollectionSpec{
							APIVersion: objectAPIVersion,
							Kind:       objectKind,
							Selector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"foo": "bar",
								},
							},
						}
						c.Name = collectionName.Name
						return nil
					},
					MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
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
						if patch != client.Apply {
							return fmt.Errorf("Expected SSA patch, but got: %v", patch)
						}
						u := obj.(*unstructured.Unstructured)
						if u.GroupVersionKind() != v1alpha2.ObjectGroupVersionKind {
							return fmt.Errorf("Expected gvk %v, but got %v", v1alpha2.ObjectGroupVersionKind, u.GroupVersionKind())
						}
						if s := sets.New[string]("col-foo0", "col-foo1"); !s.Has(u.GetName()) {
							return fmt.Errorf("Expecting one of %v, but got %v", s.UnsortedList(), u.GetName())
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
						if cnd := c.Status.GetCondition(xpv1.TypeSynced); cnd.Status != corev1.ConditionTrue {
							return fmt.Errorf("Object sync condition not true: %v", cnd.Message)
						}
						if cnd := c.Status.GetCondition(xpv1.TypeReady); cnd.Status != corev1.ConditionTrue {
							return fmt.Errorf("Object ready condition not true: %v", cnd.Message)
						}

						if l := len(c.Status.Objects); l != 2 {
							return fmt.Errorf("Expected 2 objects refs, but got %v", l)
						}
						return nil
					},
				},
			},
			want: want{
				r: reconcile.Result{RequeueAfter: pollIterval},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &Reconciler{
				client: tc.args.client,
				log:    logging.NewNopLogger(),
				clientForProvider: func(ctx context.Context, inclusterClient client.Client, providerConfigName string) (client.Client, error) {
					return tc.args.client, nil
				},
				observedObjectName: func(collection client.Object, matchedObject client.Object) (string, error) {
					return fmt.Sprintf("%s-%s", collection.GetName(), matchedObject.GetName()), nil
				},
				pollInterval: func() time.Duration {
					return pollIterval
				},
				//pollInterval:       tt.fields.pollInterval,
				//clientForProvider:  tt.fields.clientForProvider,
				//observedObjectName: tt.fields.observedObjectName,
			}
			got, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: collectionName})
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nr.Reconcile(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.r, got, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nr.Reconcile(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}