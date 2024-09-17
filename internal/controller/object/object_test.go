/*
Copyright 2021 The Crossplane Authors.

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
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
	kubernetesv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
	"github.com/crossplane-contrib/provider-kubernetes/internal/controller/object/fake"
	kubeclient "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

const (
	providerName = "kubernetes-test"

	testObjectName          = "test-object"
	testNamespace           = "test-namespace"
	testReferenceObjectName = "test-ref-object"
	testSecretName          = "testcreds"

	externalResourceName = "crossplane-system"

	someUID = "some-uid"
)

var (
	externalResourceRaw = []byte(fmt.Sprintf(`{
		"apiVersion": "v1",
		"kind": "Namespace",
		"metadata": {
			"name": %q
		}
	}`, externalResourceName))

	errBoom = errors.New("boom")
)

type notKubernetesObject struct {
	resource.Managed
}

type kubernetesObjectModifier func(obj *v1alpha2.Object)
type externalResourceModifier func(res *unstructured.Unstructured)

func kubernetesObject(om ...kubernetesObjectModifier) *v1alpha2.Object {
	o := &v1alpha2.Object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha2.SchemeGroupVersion.String(),
			Kind:       v1alpha2.ObjectKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      testObjectName,
			Namespace: testNamespace,
		},
		Spec: v1alpha2.ObjectSpec{
			ResourceSpec: xpv1.ResourceSpec{
				ProviderConfigReference: &xpv1.Reference{
					Name: providerName,
				},
				ManagementPolicies: xpv1.ManagementPolicies{xpv1.ManagementActionAll},
			},
			ForProvider: v1alpha2.ObjectParameters{
				Manifest: runtime.RawExtension{Raw: externalResourceRaw},
			},
		},
		Status: v1alpha2.ObjectStatus{},
	}

	for _, m := range om {
		m(o)
	}

	return o
}

func externalResource(rm ...externalResourceModifier) *unstructured.Unstructured {
	res := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": externalResourceName,
			},
		},
	}

	for _, m := range rm {
		m(res)
	}

	return res
}

func objectReferences() []v1alpha2.Reference {
	dependsOn := v1alpha2.DependsOn{
		APIVersion: v1alpha2.SchemeGroupVersion.String(),
		Kind:       v1alpha2.ObjectKind,
		Name:       testReferenceObjectName,
		Namespace:  testNamespace,
	}
	ref := []v1alpha2.Reference{
		{
			PatchesFrom: &v1alpha2.PatchesFrom{
				DependsOn: dependsOn,
			},
		},
		{
			DependsOn: &dependsOn,
		},
	}
	return ref
}

func referenceObject(rm ...externalResourceModifier) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": v1alpha2.SchemeGroupVersion.String(),
			"kind":       v1alpha2.ObjectKind,
			"metadata": map[string]interface{}{
				"name":      testReferenceObjectName,
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{
				"forProvider": map[string]interface{}{
					"manifest": map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "ConfigMap",
						"metadata": map[string]interface{}{
							"namespace": testNamespace,
							"labels": map[string]interface{}{
								"app": "foo",
							},
						},
					},
				},
			},
		},
	}

	for _, m := range rm {
		m(obj)
	}

	return obj
}

func referenceObjectWithFinalizer(val interface{}) *unstructured.Unstructured {
	res := referenceObject(func(res *unstructured.Unstructured) {
		metadata := res.Object["metadata"].(map[string]interface{})
		metadata["finalizers"] = []interface{}{val}
	})
	return res
}

func TestConnect(t *testing.T) {
	providerConfig := kubernetesv1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: providerName},
		Spec: kconfig.ProviderConfigSpec{
			Credentials: kconfig.ProviderCredentials{},
			Identity: &kconfig.Identity{
				Type: kconfig.IdentityTypeGoogleApplicationCredentials,
			},
		},
	}

	providerConfigGoogleInjectedIdentity := *providerConfig.DeepCopy()
	providerConfigGoogleInjectedIdentity.Spec.Identity.Source = xpv1.CredentialsSourceInjectedIdentity

	providerConfigAzure := &kubernetesv1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: providerName},
		Spec: kconfig.ProviderConfigSpec{
			Credentials: kconfig.ProviderCredentials{
				Source: xpv1.CredentialsSourceNone,
			},
			Identity: &kconfig.Identity{
				Type: kconfig.IdentityTypeAzureServicePrincipalCredentials,
				ProviderCredentials: kconfig.ProviderCredentials{
					Source: xpv1.CredentialsSourceNone,
				},
			},
		},
	}

	providerConfigAzureInjectedIdentity := *providerConfigAzure.DeepCopy()
	providerConfigAzureInjectedIdentity.Spec.Identity.Source = xpv1.CredentialsSourceInjectedIdentity

	providerConfigUnknownIdentitySource := *providerConfigAzure.DeepCopy()
	providerConfigUnknownIdentitySource.Spec.Identity.Type = "foo"

	type args struct {
		client            client.Client
		clientForProvider client.Client
		usage             resource.Tracker
		mg                resource.Managed
	}
	type want struct {
		err error
	}
	cases := map[string]struct {
		args
		want
	}{
		"NotKubernetesObject": {
			args: args{
				mg: notKubernetesObject{},
			},
			want: want{
				err: errors.New(errNotKubernetesObject),
			},
		},
		"FailedToTrackUsage": {
			args: args{
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return errBoom }),
				mg:    kubernetesObject(),
			},
			want: want{
				err: errors.Wrap(errBoom, errTrackPCUsage),
			},
		},
		"Success": {
			args: args{
				client: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						*obj.(*kubernetesv1alpha1.ProviderConfig) = providerConfig
						return nil
					}),
				},
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return nil }),
				mg:    kubernetesObject(),
			},
			want: want{
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &connector{
				logger: logging.NewNopLogger(),
				kube:   tc.args.client,
				clientBuilder: kubeclient.BuilderFn(func(ctx context.Context, pc kconfig.ProviderConfigSpec) (client.Client, *rest.Config, error) {
					return tc.args.clientForProvider, nil, nil
				}),
				usage: tc.usage,
			}
			_, gotErr := c.Connect(context.Background(), tc.args.mg)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("Connect(...): -want error, +got error: %s", diff)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	type args struct {
		client resource.ClientApplicator
		syncer ResourceSyncer
		mg     resource.Managed
	}
	type want struct {
		out managed.ExternalObservation
		err error
	}
	cases := map[string]struct {
		args
		want
	}{
		"NotKubernetesObject": {
			args: args{
				mg: notKubernetesObject{},
			},
			want: want{
				err: errors.New(errNotKubernetesObject),
			},
		},
		"NoKubernetesObjectExists": {
			args: args{
				mg: kubernetesObject(),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(kerrors.NewNotFound(schema.GroupResource{}, "")),
					},
				},
			},
			want: want{
				out: managed.ExternalObservation{ResourceExists: false},
				err: nil,
			},
		},
		"NotAValidManifest": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ForProvider.Manifest.Raw = []byte(`{"test": "not-a-valid-manifest"}`)
				}),
			},
			want: want{
				err: errors.Wrap(errors.Errorf(`Object 'Kind' is missing in '{"test": "not-a-valid-manifest"}'`), errUnmarshalTemplate),
			},
		},
		"FailedToGet": {
			args: args{
				mg: kubernetesObject(),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(errBoom),
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errGetObject),
			},
		},
		"NotUpToDate": {
			args: args{
				mg: kubernetesObject(),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *externalResource(func(res *unstructured.Unstructured) {
								res.SetLabels(map[string]string{"a-new-label": "foo"})
							})
							return nil
						}),
					},
				},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return current, nil
					},
					GetDesiredStateFn: func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return manifest, nil
					},
				},
			},
			want: want{
				out: managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false},
				err: nil,
			},
		},
		"UpToDate": {
			args: args{
				mg: kubernetesObject(),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *externalResource()
							return nil
						}),
					},
				},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return current, nil
					},
					GetDesiredStateFn: func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return manifest, nil
					},
				},
			},
			want: want{
				out: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{},
				},
				err: nil,
			},
		},
		"FailedToPatchFieldFromReferenceObject": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
					obj.Spec.References[0].PatchesFrom.FieldPath = ptr.To("nonexistent_field")
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *referenceObject()
							return nil
						}),
					},
				},
			},
			want: want{
				err: errors.Wrap(
					errors.Wrap(errors.Errorf(`nonexistent_field: no such field`),
						errPatchFromReferencedResource), errResolveResourceReferences),
			},
		},
		"NoReferenceObjectExists": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(errBoom),
					},
				},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return current, nil
					},
					GetDesiredStateFn: func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return manifest, nil
					},
				},
			},
			want: want{
				err: errors.Wrap(
					errors.Wrap(errBoom,
						errGetReferencedResource), errResolveResourceReferences),
			},
		},
		"NoExternalResourceExistsIfObjectWasDeleted": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.ObjectMeta.DeletionTimestamp = &metav1.Time{Time: time.Now()}
					obj.Spec.References = objectReferences()
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
							if key.Name == testReferenceObjectName {
								*obj.(*unstructured.Unstructured) = *referenceObject()
								return nil
							} else if key.Name == externalResourceName {
								return kerrors.NewNotFound(schema.GroupResource{}, "")
							}
							return errBoom
						},
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
			},
			want: want{
				out: managed.ExternalObservation{ResourceExists: false},
				err: nil,
			},
		},
		"ReferenceToObject": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
							if key.Name == testReferenceObjectName {
								*obj.(*unstructured.Unstructured) = *referenceObject()
								return nil
							} else if key.Name == externalResourceName {
								*obj.(*unstructured.Unstructured) = *externalResource()
								return nil
							}
							return errBoom
						},
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return current, nil
					},
					GetDesiredStateFn: func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return manifest, nil
					},
				},
			},
			want: want{
				out: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{},
				},
				err: nil,
			},
		},
		"ConnectionDetails": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
					obj.Spec.ConnectionDetails = []v1alpha2.ConnectionDetail{
						{
							ObjectReference: corev1.ObjectReference{
								Kind:       "Secret",
								Namespace:  testNamespace,
								Name:       testSecretName,
								APIVersion: "v1",
								FieldPath:  "data.db-password",
							},
							ToConnectionSecretKey: "password",
						},
					}
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
							switch key.Name {
							case externalResourceName:
								*obj.(*unstructured.Unstructured) = *externalResource()
							case testSecretName:
								*obj.(*unstructured.Unstructured) = unstructured.Unstructured{
									Object: map[string]interface{}{
										"data": map[string]interface{}{
											"db-password": "MTIzNDU=",
										},
									},
								}
							}
							return nil
						},
					},
				},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return current, nil
					},
					GetDesiredStateFn: func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return manifest, nil
					},
				},
			},
			want: want{
				out: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{"password": []byte("12345")},
				},
				err: nil,
			},
		},
		"FailedToGetConnectionDetails": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
					obj.Spec.ConnectionDetails = []v1alpha2.ConnectionDetail{
						{
							ObjectReference: corev1.ObjectReference{
								Kind:       "Secret",
								Namespace:  testNamespace,
								Name:       testSecretName,
								APIVersion: "v1",
								FieldPath:  "data.db-password",
							},
							ToConnectionSecretKey: "password",
						},
					}
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
							switch key.Name {
							case externalResourceName:
								*obj.(*unstructured.Unstructured) = *externalResource()
							case testSecretName:
								return errBoom
							}
							return nil
						},
					},
				},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return current, nil
					},
					GetDesiredStateFn: func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return manifest, nil
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errBoom, errGetObject), errGetConnectionDetails),
			},
		},
		"Observe Only - up to date by default": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionObserve}
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *externalResource()
							return nil
						}),
					},
				},
				syncer: &fake.ResourceSyncer{
					GetObservedStateFn: func(ctx context.Context, obj *v1alpha2.Object, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return current, nil
					},
					GetDesiredStateFn: func(ctx context.Context, obj *v1alpha2.Object, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return manifest, nil
					},
				},
			},
			want: want{
				out: managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true, ConnectionDetails: managed.ConnectionDetails{}},
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{
				logger:      logging.NewNopLogger(),
				client:      tc.args.client,
				localClient: tc.args.client,
				syncer:      tc.args.syncer,
			}
			got, gotErr := e.Observe(context.Background(), tc.args.mg)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("e.Observe(...): -want error, +got error: %s", diff)
			}

			if diff := cmp.Diff(tc.want.out, got); diff != "" {
				t.Fatalf("e.Observe(...): -want out, +got out: %s", diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	type args struct {
		mg     resource.Managed
		syncer ResourceSyncer
	}
	type want struct {
		out managed.ExternalCreation
		err error
	}
	cases := map[string]struct {
		args
		want
	}{
		"NotKubernetesObject": {
			args: args{
				mg: notKubernetesObject{},
			},
			want: want{
				err: errors.New(errNotKubernetesObject),
			},
		},
		"NotAValidManifest": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ForProvider.Manifest.Raw = []byte(`{"test": "not-a-valid-manifest"}`)
				}),
			},
			want: want{
				err: errors.Wrap(errors.Errorf(`Object 'Kind' is missing in '{"test": "not-a-valid-manifest"}'`), errUnmarshalTemplate),
			},
		},
		"FailedToCreate": {
			args: args{
				mg: kubernetesObject(),
				syncer: &fake.ResourceSyncer{
					SyncResourceFn: func(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return nil, errBoom
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateObject),
			},
		},
		"SuccessDefaultsToObjectName": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ForProvider.Manifest.Raw = []byte(`{
				    "apiVersion": "v1",
				    "kind": "Namespace" }`)
				}),
				syncer: &fake.ResourceSyncer{
					SyncResourceFn: func(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						if desired.GetName() != testObjectName {
							t.Errorf("Name should default to object name when not provider in manifest")
						}
						return desired, nil
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"Success": {
			args: args{
				mg: kubernetesObject(),
				syncer: &fake.ResourceSyncer{
					SyncResourceFn: func(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return desired, nil
					},
				},
			},
			want: want{
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{
				logger: logging.NewNopLogger(),
				syncer: tc.args.syncer,
			}
			got, gotErr := e.Create(context.Background(), tc.args.mg)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("e.Create(...): -want error, +got error: %s", diff)
			}

			if diff := cmp.Diff(tc.want.out, got); diff != "" {
				t.Fatalf("e.Create(...): -want out, +got out: %s", diff)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	type args struct {
		mg     resource.Managed
		syncer ResourceSyncer
	}
	type want struct {
		out managed.ExternalUpdate
		err error
	}
	cases := map[string]struct {
		args
		want
	}{
		"NotKubernetesObject": {
			args: args{
				mg: notKubernetesObject{},
			},
			want: want{
				err: errors.New(errNotKubernetesObject),
			},
		},
		"NotAValidManifest": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ForProvider.Manifest.Raw = []byte(`{"test": "not-a-valid-manifest"}`)
				}),
			},
			want: want{
				err: errors.Wrap(errors.Errorf(`Object 'Kind' is missing in '{"test": "not-a-valid-manifest"}'`), errUnmarshalTemplate),
			},
		},
		"FailedToApply": {
			args: args{
				mg: kubernetesObject(),
				syncer: &fake.ResourceSyncer{
					SyncResourceFn: func(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return nil, errBoom
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errApplyObject),
			},
		},
		"SuccessDefaultsToObjectName": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ForProvider.Manifest.Raw = []byte(`{
				    "apiVersion": "v1",
				    "kind": "Namespace" }`)
				}),
				syncer: &fake.ResourceSyncer{
					SyncResourceFn: func(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						if desired.GetName() != testObjectName {
							t.Errorf("Name should default to object name when not provider in manifest")
						}
						return desired, nil
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"Success": {
			args: args{
				mg: kubernetesObject(),
				syncer: &fake.ResourceSyncer{
					SyncResourceFn: func(ctx context.Context, obj *v1alpha2.Object, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						return desired, nil
					},
				},
			},
			want: want{
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{
				logger: logging.NewNopLogger(),
				syncer: tc.args.syncer,
			}
			got, gotErr := e.Update(context.Background(), tc.args.mg)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("e.Update(...): -want error, +got error: %s", diff)
			}

			if diff := cmp.Diff(tc.want.out, got); diff != "" {
				t.Fatalf("e.Update(...): -want out, +got out: %s", diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	type args struct {
		client resource.ClientApplicator
		mg     resource.Managed
	}
	type want struct {
		err error
	}
	cases := map[string]struct {
		args
		want
	}{
		"NotKubernetesObject": {
			args: args{
				mg: notKubernetesObject{},
			},
			want: want{
				err: errors.New(errNotKubernetesObject),
			},
		},
		"NotAValidManifest": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ForProvider.Manifest.Raw = []byte(`{"test": "not-a-valid-manifest"}`)
				}),
			},
			want: want{
				err: errors.Wrap(errors.Errorf(`Object 'Kind' is missing in '{"test": "not-a-valid-manifest"}'`), errUnmarshalTemplate),
			},
		},
		"FailedToDelete": {
			args: args{
				mg: kubernetesObject(),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockDelete: test.NewMockDeleteFn(errBoom),
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errDeleteObject),
			},
		},
		"SuccessDefaultsToObjectName": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.ForProvider.Manifest.Raw = []byte(`{
				    "apiVersion": "v1",
				    "kind": "Namespace" }`)
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockDelete: test.NewMockDeleteFn(nil, func(obj client.Object) error {
							if obj.GetName() != testObjectName {
								t.Errorf("Name should default to object name when not provider in manifest")
							}
							return nil
						}),
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"Success": {
			args: args{
				mg: kubernetesObject(),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockDelete: test.NewMockDeleteFn(nil),
					},
				},
			},
			want: want{
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{
				logger: logging.NewNopLogger(),
				client: tc.args.client,
			}
			gotErr := e.Delete(context.Background(), tc.args.mg)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("e.Delete(...): -want error, +got error: %s", diff)
			}
		})
	}
}

func TestAddFinalizer(t *testing.T) {
	type args struct {
		client resource.ClientApplicator
		mg     resource.Managed
	}
	type want struct {
		err error
	}
	cases := map[string]struct {
		args
		want
	}{
		"NotKubernetesObject": {
			args: args{
				mg: notKubernetesObject{},
			},
			want: want{
				err: errors.New(errNotKubernetesObject),
			},
		},
		"FailedToAddObjectFinalizer": {
			args: args{
				mg: kubernetesObject(),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockUpdate: test.NewMockUpdateFn(errBoom),
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errAddFinalizer),
			},
		},
		"ObjectFinalizerExists": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.ObjectMeta.Finalizers = append(obj.ObjectMeta.Finalizers, objFinalizerName)
				}),
			},
			want: want{
				err: nil,
			},
		},
		"NoReferenceObjectExists": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet:    test.NewMockGetFn(errBoom),
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
			},
			want: want{
				err: errors.Wrap(
					errors.Wrap(errBoom,
						errGetReferencedResource), errAddFinalizer),
			},
		},
		"EmptyReference": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = []v1alpha2.Reference{{}}
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"FailedToAddReferenceFinalizer": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *referenceObject()
							return nil
						}),
						MockUpdate: test.NewMockUpdateFn(nil, func(obj client.Object) error {
							name := obj.GetName()
							if name == testReferenceObjectName {
								return errBoom
							}
							return nil
						}),
					},
				},
			},
			want: want{
				err: errors.Wrap(
					errors.Wrap(errBoom,
						errAddReferenceFinalizer), errAddFinalizer),
			},
		},
		"Success": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.Spec.References = objectReferences()
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet:    test.NewMockGetFn(nil),
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
			},
			want: want{
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &objFinalizer{
				client: tc.args.client,
			}
			gotErr := f.AddFinalizer(context.Background(), tc.args.mg)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("f.AddFinalizer(...): -want error, +got error: %s", diff)
			}
		})
	}
}

func TestRemoveFinalizer(t *testing.T) {
	type args struct {
		client resource.ClientApplicator
		mg     resource.Managed
	}
	type want struct {
		err        error
		finalizers []string
	}
	cases := map[string]struct {
		args
		want
	}{
		"NotKubernetesObject": {
			args: args{
				mg: notKubernetesObject{},
			},
			want: want{
				err:        errors.New(errNotKubernetesObject),
				finalizers: []string{},
			},
		},
		"FailedToRemoveObjectFinalizer": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.ObjectMeta.Finalizers = append(obj.ObjectMeta.Finalizers, objFinalizerName)
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockUpdate: test.NewMockUpdateFn(errBoom),
					},
				},
			},
			want: want{
				err:        errors.Wrap(errBoom, errRemoveFinalizer),
				finalizers: []string{},
			},
		},
		"NoObjectFinalizerExists": {
			args: args{
				mg: kubernetesObject(),
			},
			want: want{
				err:        nil,
				finalizers: nil,
			},
		},
		"NoReferenceFinalizerExists": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.ObjectMeta.Finalizers = append(obj.ObjectMeta.Finalizers, objFinalizerName)
					obj.Spec.References = objectReferences()
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *referenceObject()
							return nil
						}),
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
			},
			want: want{
				err:        nil,
				finalizers: []string{},
			},
		},
		"ReferenceNotFound": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.ObjectMeta.Finalizers = append(obj.ObjectMeta.Finalizers, objFinalizerName)
					obj.Spec.References = objectReferences()
					obj.ObjectMeta.UID = someUID
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet:    test.NewMockGetFn(kerrors.NewNotFound(schema.GroupResource{}, testReferenceObjectName)),
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
			},
			want: want{
				err:        nil,
				finalizers: []string{},
			},
		},
		"FailedToRemoveReferenceFinalizer": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.ObjectMeta.Finalizers = append(obj.ObjectMeta.Finalizers, objFinalizerName)
					obj.Spec.References = objectReferences()
					obj.ObjectMeta.UID = someUID
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *referenceObjectWithFinalizer(refFinalizerNamePrefix + someUID)
							return nil
						},
						MockUpdate: test.NewMockUpdateFn(nil, func(obj client.Object) error {
							name := obj.GetName()
							if name == testReferenceObjectName {
								return errBoom
							}
							return nil
						}),
					},
				},
			},
			want: want{
				err: errors.Wrap(
					errors.Wrap(errBoom,
						errRemoveReferenceFinalizer), errRemoveFinalizer),
				finalizers: []string{objFinalizerName},
			},
		},
		"Success": {
			args: args{
				mg: kubernetesObject(func(obj *v1alpha2.Object) {
					obj.ObjectMeta.Finalizers = append(obj.ObjectMeta.Finalizers, objFinalizerName)
					obj.Spec.References = objectReferences()
					obj.ObjectMeta.UID = someUID
				}),
				client: resource.ClientApplicator{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*unstructured.Unstructured) = *referenceObjectWithFinalizer(refFinalizerNamePrefix + someUID)
							return nil
						}),
						MockUpdate: test.NewMockUpdateFn(nil),
					},
				},
			},
			want: want{
				err:        nil,
				finalizers: []string{},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &objFinalizer{
				client: tc.args.client,
			}

			gotErr := f.RemoveFinalizer(context.Background(), tc.args.mg)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Errorf("f.RemoveFinalizer(...): -want error, +got error: %s", diff)
			}

			if _, ok := tc.args.mg.(*v1alpha2.Object); ok {
				sort := cmpopts.SortSlices(func(a, b string) bool { return a < b })
				if diff := cmp.Diff(tc.want.finalizers, tc.args.mg.GetFinalizers(), sort); diff != "" {
					t.Errorf("managed resource finalizers: -want, +got: %s", diff)
				}
			}
		})
	}
}

func TestConnectionDetails(t *testing.T) {
	mockClient := func(secretData map[string]interface{}, err error) *test.MockClient {
		return &test.MockClient{
			MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
				if o, ok := obj.(*unstructured.Unstructured); o.GetKind() == "Secret" && ok && key.Name == testSecretName && key.Namespace == testNamespace {
					*obj.(*unstructured.Unstructured) = unstructured.Unstructured{
						Object: map[string]interface{}{
							"data": secretData,
						},
					}
				}
				return err
			},
		}
	}

	connDetail := v1alpha2.ConnectionDetail{
		ObjectReference: corev1.ObjectReference{
			Kind:       "Secret",
			Namespace:  testNamespace,
			Name:       testSecretName,
			APIVersion: "v1",
			FieldPath:  "data.db-password",
		},
		ToConnectionSecretKey: "password",
	}

	type args struct {
		kube        client.Client
		connDetails []v1alpha2.ConnectionDetail
	}
	type want struct {
		out managed.ConnectionDetails
		err error
	}
	cases := map[string]struct {
		args
		want
	}{
		"Fail_ObjectNotExisting": {
			args: args{
				kube: mockClient(
					map[string]interface{}{},
					kerrors.NewNotFound(schema.GroupResource{Group: "", Resource: "secrets"}, testSecretName),
				),
				connDetails: []v1alpha2.ConnectionDetail{connDetail},
			},
			want: want{
				out: managed.ConnectionDetails{},
				err: errors.Wrap(kerrors.NewNotFound(schema.GroupResource{Group: "", Resource: "secrets"}, testSecretName), errGetObject),
			},
		},
		"Fail_FieldPathNotExisting": {
			args: args{
				kube: mockClient(
					map[string]interface{}{
						"non-db-password": "MTIzNDU=",
					},
					nil,
				),
				connDetails: []v1alpha2.ConnectionDetail{connDetail},
			},
			want: want{
				out: managed.ConnectionDetails{},
				err: errors.Wrap(errors.Errorf("data.db-password: no such field"), errGetValueAtFieldPath),
			},
		},
		"Fail_InvalidEncoding": {
			args: args{
				kube: mockClient(
					map[string]interface{}{
						"db-password": "_messed_up_encoding",
					},
					nil,
				),
				connDetails: []v1alpha2.ConnectionDetail{connDetail},
			},
			want: want{
				out: managed.ConnectionDetails{},
				err: errors.Wrap(base64.CorruptInputError(0), errDecodeSecretData),
			},
		},
		"Success": {
			args: args{
				kube: mockClient(
					map[string]interface{}{
						"db-password": "MTIzNDU=",
					},
					nil,
				),
				connDetails: []v1alpha2.ConnectionDetail{connDetail},
			},
			want: want{
				out: managed.ConnectionDetails{
					"password": []byte("12345"),
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, gotErr := connectionDetails(context.Background(), tc.args.kube, tc.args.connDetails)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("connectionDetails(...): -want error, +got error: %s", diff)
			}
			if diff := cmp.Diff(tc.want.out, got); diff != "" {
				t.Errorf("connectionDetails(...): -want result, +got result: %s", diff)
			}
		})
	}
}

func TestUpdateConditionFromObserved(t *testing.T) {
	type args struct {
		obj      *v1alpha2.Object
		observed *unstructured.Unstructured
	}
	type want struct {
		err        error
		conditions []xpv1.Condition
	}
	cases := map[string]struct {
		args
		want
	}{
		"NoopIfNoPolicyDefined": {
			args: args{
				obj: &v1alpha2.Object{},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{},
					},
				},
			},
			want: want{
				err:        nil,
				conditions: nil,
			},
		},
		"NoopIfSuccessfulCreatePolicyDefined": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicySuccessfulCreate,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{},
					},
				},
			},
			want: want{
				err:        nil,
				conditions: nil,
			},
		},
		"UnavailableIfDeriveFromObjectAndNotReady": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyDeriveFromObject,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{
							Conditions: []xpv1.Condition{
								{
									Type:   xpv1.TypeReady,
									Status: corev1.ConditionFalse,
								},
							},
						},
					},
				},
			},
			want: want{
				err: nil,
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionFalse,
						Reason: xpv1.ReasonUnavailable,
					},
				},
			},
		},
		"UnavailableIfDerivedFromObjectAndNoCondition": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyDeriveFromObject,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{},
					},
				},
			},
			want: want{
				err: nil,
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionFalse,
						Reason: xpv1.ReasonUnavailable,
					},
				},
			},
		},
		"AvailableIfDeriveFromObjectAndReady": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyDeriveFromObject,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{
							Conditions: []xpv1.Condition{
								{
									Type:   xpv1.TypeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			want: want{
				err: nil,
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionTrue,
						Reason: xpv1.ReasonAvailable,
					},
				},
			},
		},
		"UnavailableIfDerivedFromObjectAndCantParse": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyDeriveFromObject,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": "not a conditioned status",
					},
				},
			},
			want: want{
				err: nil,
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionFalse,
						Reason: xpv1.ReasonUnavailable,
					},
				},
			},
		},
		"UnavailableIfAllTrueWithoutConditions": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyAllTrue,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionFalse,
						Reason: xpv1.ReasonUnavailable,
					},
				},
			},
		},
		"UnavailableIfAllTrueAndCantParse": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyAllTrue,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": "not a conditioned status",
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionFalse,
						Reason: xpv1.ReasonUnavailable,
					},
				},
			},
		},
		"UnavailableIfAllTrueAndAnyConditionFalse": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyAllTrue,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{
							Conditions: []xpv1.Condition{
								{
									Type:   "condition1",
									Status: corev1.ConditionFalse,
								},
								{
									Type:   xpv1.TypeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionFalse,
						Reason: xpv1.ReasonUnavailable,
					},
				},
			},
		},
		"AvailableIfAllTrueAndAllConditionsTrue": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy: v1alpha2.ReadinessPolicyAllTrue,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{
							Conditions: []xpv1.Condition{
								{
									Type:   "condition1",
									Status: corev1.ConditionTrue,
								},
								{
									Type:   xpv1.TypeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionTrue,
						Reason: xpv1.ReasonAvailable,
					},
				},
			},
		},
		"AvailableIfCelAllMacroTrue": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy:   v1alpha2.ReadinessPolicyDeriveFromCelQuery,
							CelQuery: `object.status.conditions.all(x, x.status == "True")`,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{
							Conditions: []xpv1.Condition{
								{
									Type:   "condition1",
									Status: corev1.ConditionTrue,
								},
								{
									Type:   xpv1.TypeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionTrue,
						Reason: xpv1.ReasonAvailable,
					},
				},
			},
		},
		"UnavailableIfCelAllMacroFalse": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy:   v1alpha2.ReadinessPolicyDeriveFromCelQuery,
							CelQuery: `object.status.conditions.all(x, x.status == "True")`,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{
							Conditions: []xpv1.Condition{
								{
									Type:   "condition1",
									Status: corev1.ConditionTrue,
								},
								{
									Type:   xpv1.TypeReady,
									Status: corev1.ConditionFalse,
								},
							},
						},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionFalse,
						Reason: xpv1.ReasonUnavailable,
					},
				},
			},
		},
		"AvailableIfCelCustomField": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy:   v1alpha2.ReadinessPolicyDeriveFromCelQuery,
							CelQuery: `object.status.isReady == true`,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": map[string]any{
							"isReady": true,
						},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Status: corev1.ConditionTrue,
						Reason: xpv1.ReasonAvailable,
					},
				},
			},
		},
		"UnavailableIfCelCustomFieldNotThere": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy:   v1alpha2.ReadinessPolicyDeriveFromCelQuery,
							CelQuery: `object.status.isReady == true`,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": map[string]any{},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:    xpv1.TypeReady,
						Status:  corev1.ConditionFalse,
						Reason:  xpv1.ReasonUnavailable,
						Message: fmt.Sprintf("%s: %s", errCelQueryFailedToEvalProgram, "no such key: isReady"),
					},
				},
			},
		},
		"AvailableIfCelQueryUsesExistsToAPath": {
			args: args{
				obj: &v1alpha2.Object{
					Spec: v1alpha2.ObjectSpec{
						Readiness: v1alpha2.Readiness{
							Policy:   v1alpha2.ReadinessPolicyDeriveFromCelQuery,
							CelQuery: `object.status.conditions.exists(c, c.type == "condition1" && c.status == "True" )`,
						},
					},
				},
				observed: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": xpv1.ConditionedStatus{
							Conditions: []xpv1.Condition{
								{
									Type:   "condition1",
									Status: corev1.ConditionTrue,
								},
								{
									Type:   xpv1.TypeReady,
									Status: corev1.ConditionFalse,
								},
							},
						},
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{
					{
						Type:   xpv1.TypeReady,
						Reason: xpv1.ReasonAvailable,
						Status: corev1.ConditionTrue,
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{
				logger: logging.NewNopLogger(),
			}
			gotErr := e.updateConditionFromObserved(tc.args.obj, tc.args.observed)
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Fatalf("updateConditionFromObserved(...): -want error, +got error: %s", diff)
			}
			if diff := cmp.Diff(tc.want.conditions, tc.args.obj.Status.Conditions, cmpopts.SortSlices(func(a, b xpv1.Condition) bool {
				return a.Type < b.Type
			}), cmpopts.IgnoreFields(xpv1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("updateConditionFromObserved(...): -want result, +got result: %s", diff)
			}
		})
	}
}
