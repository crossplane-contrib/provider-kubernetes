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

package client

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

func kubeconfigFor(server string) string {
	return fmt.Sprintf(`apiVersion: v1
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
  user:
    token: t
`, server)
}

func secretLocalClient(kubeconfigs map[string]string) client.Client {
	return &test.MockClient{
		MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
			s, ok := obj.(*corev1.Secret)
			if !ok {
				return fmt.Errorf("unexpected object type %T", obj)
			}
			kc, ok := kubeconfigs[key.Name]
			if !ok {
				return fmt.Errorf("no kubeconfig for secret %q", key.Name)
			}
			s.Data = map[string][]byte{"kubeconfig": []byte(kc)}
			return nil
		},
	}
}

func pcSpec(secretName string) kconfig.ProviderConfigSpec {
	return kconfig.ProviderConfigSpec{
		Credentials: kconfig.ProviderCredentials{
			Source: xpv2.CredentialsSourceSecret,
			CommonCredentialSelectors: xpv2.CommonCredentialSelectors{
				SecretRef: &xpv2.SecretKeySelector{
					SecretReference: xpv2.SecretReference{
						Name:      secretName,
						Namespace: types.NamespacedName{Namespace: "crossplane-system"}.Namespace,
					},
					Key: "kubeconfig",
				},
			},
		},
	}
}

func TestKubeForProviderConfigCaching(t *testing.T) {
	type args struct {
		kubeconfigs map[string]string
		firstPC     kconfig.ProviderConfigSpec
		secondPC    kconfig.ProviderConfigSpec
	}
	type want struct {
		sameClient bool
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"SameCredentialsReuseClient": {
			args: args{
				kubeconfigs: map[string]string{
					"creds": kubeconfigFor("https://example.org:6443"),
				},
				firstPC:  pcSpec("creds"),
				secondPC: pcSpec("creds"),
			},
			want: want{sameClient: true},
		},
		"DifferentCredentialsBuildNewClient": {
			args: args{
				kubeconfigs: map[string]string{
					"creds-a": kubeconfigFor("https://a.example.org:6443"),
					"creds-b": kubeconfigFor("https://b.example.org:6443"),
				},
				firstPC:  pcSpec("creds-a"),
				secondPC: pcSpec("creds-b"),
			},
			want: want{sameClient: false},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b := NewIdentityAwareBuilder(secretLocalClient(tc.args.kubeconfigs))

			first, _, err := b.KubeForProviderConfig(context.Background(), tc.args.firstPC)
			if err != nil {
				t.Fatalf("first KubeForProviderConfig(...): unexpected error: %v", err)
			}
			second, _, err := b.KubeForProviderConfig(context.Background(), tc.args.secondPC)
			if err != nil {
				t.Fatalf("second KubeForProviderConfig(...): unexpected error: %v", err)
			}

			if got := first == second; got != tc.want.sameClient {
				t.Errorf("KubeForProviderConfig(...): same client = %v, want %v", got, tc.want.sameClient)
			}
		})
	}
}

func TestClientCacheEviction(t *testing.T) {
	c := newClientCache(2)
	c.put("a", cachedClient{})
	c.put("b", cachedClient{})
	if _, ok := c.get("a"); !ok { // refresh "a" so "b" is least recently used
		t.Fatal("expected key a to be cached")
	}
	c.put("c", cachedClient{})

	if _, ok := c.get("b"); ok {
		t.Error("expected least recently used key b to be evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("expected key %s to remain cached", k)
		}
	}
}
