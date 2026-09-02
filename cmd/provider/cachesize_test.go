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

package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/cluster/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/v1alpha1"
	kubeclient "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

var errBoom = errors.New("boom")

// specFor references the kubeconfig key of the named Secret.
func specFor(secret string) kconfig.ProviderConfigSpec {
	return kconfig.ProviderConfigSpec{
		Credentials: kconfig.ProviderCredentials{
			Source: xpv2.CredentialsSourceSecret,
			CommonCredentialSelectors: xpv2.CommonCredentialSelectors{
				SecretRef: &xpv2.SecretKeySelector{
					SecretReference: xpv2.SecretReference{Name: secret, Namespace: "crossplane-system"},
					Key:             "kubeconfig",
				},
			},
		},
	}
}

func kubeconfigFor(server string) []byte {
	return fmt.Appendf(nil, `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: %s
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
users:
- name: u
  user: {}
`, server)
}

// listProviderConfigs serves one ProviderConfig of every kind, referencing
// the named Secrets in order.
func listProviderConfigs(legacy, namespaced, cluster string) test.MockListFn {
	return func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
		switch l := list.(type) {
		case *clusterv1alpha1.ProviderConfigList:
			l.Items = []clusterv1alpha1.ProviderConfig{{Spec: specFor(legacy)}}
		case *namespacedv1alpha1.ProviderConfigList:
			l.Items = []namespacedv1alpha1.ProviderConfig{{Spec: specFor(namespaced)}}
		case *namespacedv1alpha1.ClusterProviderConfigList:
			l.Items = []namespacedv1alpha1.ClusterProviderConfig{{Spec: specFor(cluster)}}
		default:
			return fmt.Errorf("unexpected list type %T", list)
		}
		return nil
	}
}

// getSecrets serves the kubeconfig key of the named Secrets.
func getSecrets(secrets map[string][]byte) test.MockGetFn {
	return func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		kc, ok := secrets[key.Name]
		if !ok {
			return errBoom
		}
		obj.(*corev1.Secret).Data = map[string][]byte{"kubeconfig": kc}
		return nil
	}
}

func TestProviderConfigSpecs(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: namespacedv1alpha1.ClusterProviderConfigGroupVersionKind.GroupKind()}

	type args struct {
		list test.MockListFn
	}
	type want struct {
		specs []kconfig.ProviderConfigSpec
		err   error
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"EveryKindContributes": {
			args: args{list: listProviderConfigs("legacy", "namespaced", "cluster")},
			want: want{specs: []kconfig.ProviderConfigSpec{specFor("legacy"), specFor("namespaced"), specFor("cluster")}},
		},
		"KindWithoutCRDContributesNone": {
			args: args{list: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*namespacedv1alpha1.ClusterProviderConfigList); ok {
					return noMatch
				}
				return listProviderConfigs("legacy", "namespaced", "")(ctx, list, opts...)
			}},
			want: want{specs: []kconfig.ProviderConfigSpec{specFor("legacy"), specFor("namespaced")}},
		},
		"ListErrorIsReturned": {
			args: args{list: func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				if _, ok := list.(*namespacedv1alpha1.ProviderConfigList); ok {
					return errBoom
				}
				return nil
			}},
			want: want{err: errors.Wrapf(errBoom, "cannot list %s", namespacedv1alpha1.ProviderConfigGroupKind)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := providerConfigSpecs(context.Background(), &test.MockClient{MockList: tc.args.list})
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("providerConfigSpecs(...): -want error, +got error:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.specs, got); diff != "" {
				t.Errorf("providerConfigSpecs(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestClientCacheSize(t *testing.T) {
	kubeconfigA := kubeconfigFor("https://a.example.org:6443")
	sharedSecrets := map[string][]byte{
		"a":      kubeconfigA,
		"a-copy": kubeconfigA,
		"b":      kubeconfigFor("https://b.example.org:6443"),
	}
	// manyProviderConfigs serves n legacy ProviderConfigs with a kubeconfig
	// each, so that the sizing rises above the default.
	manySecrets := map[string][]byte{}
	manyProviderConfigs := func(n int) test.MockListFn {
		return func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
			if l, ok := list.(*clusterv1alpha1.ProviderConfigList); ok {
				for i := range n {
					name := fmt.Sprintf("kubeconfig-%d", i)
					manySecrets[name] = kubeconfigFor(fmt.Sprintf("https://%d.example.org:6443", i))
					l.Items = append(l.Items, clusterv1alpha1.ProviderConfig{Spec: specFor(name)})
				}
			}
			return nil
		}
	}

	type args struct {
		kube client.Client
	}
	type want struct {
		size int
		err  bool
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"ListFailureIsAnError": {
			args: args{kube: &test.MockClient{MockList: test.NewMockListFn(errBoom), MockGet: getSecrets(sharedSecrets)}},
			want: want{err: true},
		},
		"SharedCredentialsStayWithinTheDefault": {
			args: args{kube: &test.MockClient{MockList: listProviderConfigs("a", "a-copy", "b"), MockGet: getSecrets(sharedSecrets)}},
			want: want{size: kubeclient.DefaultClientCacheSize},
		},
		"ManyCredentialSetsGetHeadroom": {
			args: args{kube: &test.MockClient{MockList: manyProviderConfigs(20), MockGet: getSecrets(manySecrets)}},
			want: want{size: 22},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := clientCacheSize(context.Background(), logging.NewNopLogger(), tc.args.kube)
			if diff := cmp.Diff(tc.want.err, err != nil); diff != "" {
				t.Fatalf("clientCacheSize(...): -want error, +got error (%v):\n%s", err, diff)
			}
			if diff := cmp.Diff(tc.want.size, got); diff != "" {
				t.Errorf("clientCacheSize(...): -want, +got:\n%s", diff)
			}
		})
	}
}
