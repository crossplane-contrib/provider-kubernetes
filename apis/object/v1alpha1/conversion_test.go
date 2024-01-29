/*
Copyright 2023 The Crossplane Authors.

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

package v1alpha1_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"

	v1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha1"
	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
)

func TestConvertTo(t *testing.T) {
	type args struct {
		src *v1alpha1.Object
	}
	type want struct {
		err error
		dst *v1alpha2.Object
	}

	tests := []struct {
		name string
		args args
		want want
	}{
		{
			name: "converts to v1alpha2",
			args: args{
				src: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ResourceSpec: v1alpha1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ConnectionDetails: []v1alpha1.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha1.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ManagementPolicy: v1alpha1.Observe,
						References: []v1alpha1.Reference{
							{
								DependsOn: &v1alpha1.DependsOn{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
									Namespace:  "coolns",
								},
								PatchesFrom: &v1alpha1.PatchesFrom{
									DependsOn: v1alpha1.DependsOn{
										APIVersion: "v1",
										Kind:       "Secret",
										Name:       "topsecret",
										Namespace:  "coolns",
									},
									FieldPath: pointer.String("data.password"),
								},
								ToFieldPath: pointer.String("data"),
							},
						},
						Readiness: v1alpha1.Readiness{Policy: v1alpha1.ReadinessPolicySuccessfulCreate},
					},
				},
			},
			want: want{
				dst: &v1alpha2.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha2.ObjectSpec{
						ResourceSpec: v1.ResourceSpec{
							DeletionPolicy:     v1.DeletionDelete,
							ManagementPolicies: []v1.ManagementAction{v1.ManagementActionObserve},
						},
						ConnectionDetails: []v1alpha2.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						References: []v1alpha2.Reference{
							{
								DependsOn: &v1alpha2.DependsOn{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
									Namespace:  "coolns",
								},
								PatchesFrom: &v1alpha2.PatchesFrom{
									DependsOn: v1alpha2.DependsOn{
										APIVersion: "v1",
										Kind:       "Secret",
										Name:       "topsecret",
										Namespace:  "coolns",
									},
									FieldPath: pointer.String("data.password"),
								},
								ToFieldPath: pointer.String("data"),
							},
						},
						Readiness: v1alpha2.Readiness{Policy: v1alpha2.ReadinessPolicySuccessfulCreate},
					},
				},
			},
		},
		{
			name: "converts to v1alpha2 - empty policy",
			args: args{
				src: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ResourceSpec: v1alpha1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ForProvider: v1alpha1.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ManagementPolicy: "",
					},
				},
			},
			want: want{
				dst: &v1alpha2.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha2.ObjectSpec{
						ResourceSpec: v1.ResourceSpec{
							DeletionPolicy:     v1.DeletionDelete,
							ManagementPolicies: []v1.ManagementAction{v1.ManagementActionAll},
						},
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ConnectionDetails: []v1alpha2.ConnectionDetail{},
						References:        []v1alpha2.Reference{},
					},
				},
			},
		},
		{
			name: "converts to v1alpha2 - nil checks",
			args: args{
				src: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ResourceSpec: v1alpha1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ConnectionDetails: []v1alpha1.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha1.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ManagementPolicy: v1alpha1.Observe,
						References: []v1alpha1.Reference{
							{
								DependsOn:   nil,
								PatchesFrom: nil,
							},
						},
						Readiness: v1alpha1.Readiness{Policy: v1alpha1.ReadinessPolicySuccessfulCreate},
					},
				},
			},
			want: want{
				dst: &v1alpha2.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha2.ObjectSpec{
						ResourceSpec: v1.ResourceSpec{
							DeletionPolicy:     v1.DeletionDelete,
							ManagementPolicies: []v1.ManagementAction{v1.ManagementActionObserve},
						},
						ConnectionDetails: []v1alpha2.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						References: []v1alpha2.Reference{
							{
								DependsOn:   nil,
								PatchesFrom: nil,
							},
						},
						Readiness: v1alpha2.Readiness{Policy: v1alpha2.ReadinessPolicySuccessfulCreate},
					},
				},
			},
		},
		{
			name: "errors if management policy is unknown",
			args: args{
				src: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ManagementPolicy: v1alpha1.ManagementPolicy("unknown"),
					},
				},
			},
			want: want{
				err: errors.New("unknown management policy: unknown"),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			beta := &v1alpha2.Object{}
			err := tc.args.src.ConvertTo(beta)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\nr.ConvertTo(...): -want error, +got error:\n%s", diff)
			}
			if err != nil {
				return
			}
			if diff := cmp.Diff(tc.want.dst, beta); diff != "" {
				t.Errorf("\nr.ConvertTo(...): -want converted, +got converted:\n%s", diff)
			}
		})
	}
}

func TestConvertFrom(t *testing.T) {
	type args struct {
		src *v1alpha2.Object
	}
	type want struct {
		err error
		dst *v1alpha1.Object
	}

	tests := []struct {
		name string
		args args
		want want
	}{
		{
			name: "converts to v1alpha1",
			args: args{
				src: &v1alpha2.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha2.ObjectSpec{
						ResourceSpec: v1.ResourceSpec{
							DeletionPolicy:     v1.DeletionDelete,
							ManagementPolicies: []v1.ManagementAction{v1.ManagementActionObserve},
						},
						ConnectionDetails: []v1alpha2.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						References: []v1alpha2.Reference{
							{
								DependsOn: &v1alpha2.DependsOn{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
									Namespace:  "coolns",
								},
								PatchesFrom: &v1alpha2.PatchesFrom{
									DependsOn: v1alpha2.DependsOn{
										APIVersion: "v1",
										Kind:       "Secret",
										Name:       "topsecret",
										Namespace:  "coolns",
									},
									FieldPath: pointer.String("data.password"),
								},
								ToFieldPath: pointer.String("data"),
							},
						},
						Readiness: v1alpha2.Readiness{Policy: v1alpha2.ReadinessPolicySuccessfulCreate},
					},
				},
			},
			want: want{
				dst: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ResourceSpec: v1alpha1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ConnectionDetails: []v1alpha1.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha1.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ManagementPolicy: v1alpha1.Observe,
						References: []v1alpha1.Reference{
							{
								DependsOn: &v1alpha1.DependsOn{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
									Namespace:  "coolns",
								},
								PatchesFrom: &v1alpha1.PatchesFrom{
									DependsOn: v1alpha1.DependsOn{
										APIVersion: "v1",
										Kind:       "Secret",
										Name:       "topsecret",
										Namespace:  "coolns",
									},
									FieldPath: pointer.String("data.password"),
								},
								ToFieldPath: pointer.String("data"),
							},
						},
						Readiness: v1alpha1.Readiness{Policy: v1alpha1.ReadinessPolicySuccessfulCreate},
					},
				},
			},
		},
		{
			name: "converts to v1alpha1 - nil checks",
			args: args{
				src: &v1alpha2.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha2.ObjectSpec{
						ResourceSpec: v1.ResourceSpec{
							DeletionPolicy:     v1.DeletionDelete,
							ManagementPolicies: []v1.ManagementAction{v1.ManagementActionObserve},
						},
						ConnectionDetails: []v1alpha2.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						References: []v1alpha2.Reference{
							{
								DependsOn:   nil,
								PatchesFrom: nil,
							},
						},
						Readiness: v1alpha2.Readiness{Policy: v1alpha2.ReadinessPolicySuccessfulCreate},
					},
				},
			},
			want: want{
				dst: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ResourceSpec: v1alpha1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ConnectionDetails: []v1alpha1.ConnectionDetail{
							{
								ObjectReference: corev1.ObjectReference{
									APIVersion: "v1",
									Kind:       "Secret",
									Name:       "topsecret",
								},
							},
						},
						ForProvider: v1alpha1.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ManagementPolicy: v1alpha1.Observe,
						References: []v1alpha1.Reference{
							{
								DependsOn:   nil,
								PatchesFrom: nil,
							},
						},
						Readiness: v1alpha1.Readiness{Policy: v1alpha1.ReadinessPolicySuccessfulCreate},
					},
				},
			},
		},
		{
			name: "converts to v1alpha1 - empty policy",
			args: args{
				src: &v1alpha2.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha2.ObjectSpec{
						ResourceSpec: v1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
					},
				},
			},
			want: want{
				dst: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ResourceSpec: v1alpha1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ForProvider: v1alpha1.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ManagementPolicy:  v1alpha1.Default,
						ConnectionDetails: []v1alpha1.ConnectionDetail{},
						References:        []v1alpha1.Reference{},
					},
				},
			},
		},
		{
			name: "converts to v1alpha1 - unsupported policy",
			args: args{
				src: &v1alpha2.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha2.ObjectSpec{
						ResourceSpec: v1.ResourceSpec{
							DeletionPolicy:     v1.DeletionDelete,
							ManagementPolicies: []v1.ManagementAction{v1.ManagementActionDelete},
						},
						ForProvider: v1alpha2.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
					},
				},
			},
			want: want{
				dst: &v1alpha1.Object{
					ObjectMeta: metav1.ObjectMeta{
						Name: "coolobject",
					},
					Spec: v1alpha1.ObjectSpec{
						ResourceSpec: v1alpha1.ResourceSpec{
							DeletionPolicy: v1.DeletionDelete,
						},
						ForProvider: v1alpha1.ObjectParameters{
							Manifest: runtime.RawExtension{Raw: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: topsecret\n")},
						},
						ManagementPolicy:  "",
						ConnectionDetails: []v1alpha1.ConnectionDetail{},
						References:        []v1alpha1.Reference{},
					},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			alpha := &v1alpha1.Object{}
			err := alpha.ConvertFrom(tc.args.src)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\nr.ConvertFrom(...): -want error, +got error:\n%s", diff)
			}
			if err != nil {
				return
			}
			if diff := cmp.Diff(tc.want.dst, alpha); diff != "" {
				t.Errorf("\nr.ConvertTo(...): -want converted, +got converted:\n%s", diff)
			}
		})
	}
}
