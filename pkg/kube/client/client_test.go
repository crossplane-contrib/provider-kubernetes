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
	"net/http"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/gke"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

func TestFromAPIConfigProxy(t *testing.T) {
	apiConfig := func(proxyURL string) *api.Config {
		return &api.Config{
			CurrentContext: "test",
			Contexts: map[string]*api.Context{
				"test": {Cluster: "test-cluster", AuthInfo: "test-user"},
			},
			Clusters: map[string]*api.Cluster{
				"test-cluster": {Server: "https://example.org:6443", ProxyURL: proxyURL},
			},
			AuthInfos: map[string]*api.AuthInfo{
				"test-user": {},
			},
		}
	}

	type args struct {
		config *api.Config
	}
	type want struct {
		proxyURL string
		err      error
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"ProxyURLSet": {
			args: args{
				config: apiConfig("http://proxy.example.org:3128"),
			},
			want: want{
				proxyURL: "http://proxy.example.org:3128",
			},
		},
		"ProxyURLUnset": {
			args: args{
				config: apiConfig(""),
			},
			want: want{},
		},
		"ProxyURLInvalid": {
			args: args{
				config: apiConfig("://invalid"),
			},
			want: want{
				err: errors.Wrap(&url.Error{Op: "parse", URL: "://invalid", Err: errors.New("missing protocol scheme")}, errParseProxyURL),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			config, err := fromAPIConfig(tc.args.config)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("fromAPIConfig() error: -want +got\n%s", diff)
			}
			if err != nil {
				return
			}

			gotProxyURL := ""
			if config.Proxy != nil {
				u, proxyErr := config.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.org:6443"}})
				if proxyErr != nil {
					t.Fatalf("unexpected error from proxy func: %v", proxyErr)
				}
				if u != nil {
					gotProxyURL = u.String()
				}
			}
			if diff := cmp.Diff(tc.want.proxyURL, gotProxyURL); diff != "" {
				t.Fatalf("fromAPIConfig() proxy URL: -want +got\n%s", diff)
			}
		})
	}
}

const testKubeconfig = `apiVersion: v1
kind: Config
current-context: test
clusters:
- name: test
  cluster:
    server: https://kubernetes.example.org
contexts:
- name: test
  context:
    cluster: test
    user: test
users:
- name: test
  user: {}
`

// gkeWrapCall records the arguments the identity injection passed to the GKE
// REST config wrapper.
type gkeWrapCall struct {
	credentials   []byte
	impersonation *gke.Impersonation
}

// stubGKEWrap replaces the gke.WrapRESTConfig indirection for the duration of
// a test and returns a pointer that captures the last call.
func stubGKEWrap(t *testing.T) *gkeWrapCall {
	t.Helper()
	call := &gkeWrapCall{}
	orig := gkeWrapRESTConfig
	gkeWrapRESTConfig = func(_ context.Context, _ *rest.Config, credentials []byte, impersonation *gke.Impersonation, _ ...string) error {
		call.credentials = credentials
		call.impersonation = impersonation
		return nil
	}
	t.Cleanup(func() { gkeWrapRESTConfig = orig })
	return call
}

// pcWithGoogleIdentity builds a ProviderConfigSpec whose kubeconfig comes from
// an environment variable, with a GoogleApplicationCredentials identity.
func pcWithGoogleIdentity(t *testing.T, identitySource xpv2.CredentialsSource, isa *kconfig.ImpersonateServiceAccountConfig) kconfig.ProviderConfigSpec {
	t.Helper()
	t.Setenv("TEST_KUBECONFIG", testKubeconfig)
	t.Setenv("TEST_GOOGLE_CREDS", "ya29.base-token")

	identity := &kconfig.Identity{
		Type:                      kconfig.IdentityTypeGoogleApplicationCredentials,
		ImpersonateServiceAccount: isa,
		ProviderCredentials: kconfig.ProviderCredentials{
			Source: identitySource,
		},
	}
	if identitySource == xpv2.CredentialsSourceEnvironment {
		identity.CommonCredentialSelectors = xpv2.CommonCredentialSelectors{
			Env: &xpv2.EnvSelector{Name: "TEST_GOOGLE_CREDS"},
		}
	}

	return kconfig.ProviderConfigSpec{
		Credentials: kconfig.ProviderCredentials{
			Source: xpv2.CredentialsSourceEnvironment,
			CommonCredentialSelectors: xpv2.CommonCredentialSelectors{
				Env: &xpv2.EnvSelector{Name: "TEST_KUBECONFIG"},
			},
		},
		Identity: identity,
	}
}

// injectForProviderConfig resolves the provider config and runs the deferred
// identity injection against the resolved REST config, mirroring what
// KubeForProviderConfig does when it builds a fresh client.
func injectForProviderConfig(t *testing.T, b *IdentityAwareBuilder, pc kconfig.ProviderConfigSpec) {
	t.Helper()
	r, err := b.resolve(context.Background(), pc)
	if err != nil {
		t.Fatalf("resolve(...): unexpected error: %v", err)
	}
	if err := r.injectIdentity(context.Background(), r.rc); err != nil {
		t.Fatalf("injectIdentity(...): unexpected error: %v", err)
	}
}

func TestResolveGoogleIdentityWiring(t *testing.T) {
	t.Run("impersonation config is mapped to the GKE wrapper", func(t *testing.T) {
		call := stubGKEWrap(t)
		pc := pcWithGoogleIdentity(t, xpv2.CredentialsSourceEnvironment, &kconfig.ImpersonateServiceAccountConfig{
			Name: "target@project.iam.gserviceaccount.com",
			Delegates: []string{
				"first@project.iam.gserviceaccount.com",
				"second@project.iam.gserviceaccount.com",
			},
		})

		b := NewIdentityAwareBuilder(nil)
		injectForProviderConfig(t, b, pc)

		want := &gke.Impersonation{
			TargetPrincipal: "target@project.iam.gserviceaccount.com",
			Delegates: []string{
				"first@project.iam.gserviceaccount.com",
				"second@project.iam.gserviceaccount.com",
			},
		}
		if diff := cmp.Diff(want, call.impersonation); diff != "" {
			t.Fatalf("impersonation not mapped, -want +got:\n%s", diff)
		}
		if string(call.credentials) != "ya29.base-token" {
			t.Fatalf("expected extracted identity credentials to be forwarded, got %q", call.credentials)
		}
	})

	t.Run("no impersonation config maps to nil", func(t *testing.T) {
		call := stubGKEWrap(t)
		pc := pcWithGoogleIdentity(t, xpv2.CredentialsSourceEnvironment, nil)

		b := NewIdentityAwareBuilder(nil)
		injectForProviderConfig(t, b, pc)

		if call.impersonation != nil {
			t.Fatalf("expected nil impersonation, got %+v", call.impersonation)
		}
	})

	t.Run("injected identity passes nil credentials with impersonation", func(t *testing.T) {
		call := stubGKEWrap(t)
		pc := pcWithGoogleIdentity(t, xpv2.CredentialsSourceInjectedIdentity, &kconfig.ImpersonateServiceAccountConfig{
			Name: "target@project.iam.gserviceaccount.com",
		})

		b := NewIdentityAwareBuilder(nil)
		injectForProviderConfig(t, b, pc)

		if call.credentials != nil {
			t.Fatalf("expected nil credentials on the injected identity path, got %q", call.credentials)
		}
		if call.impersonation == nil || call.impersonation.TargetPrincipal != "target@project.iam.gserviceaccount.com" {
			t.Fatalf("impersonation not mapped on the injected identity path: %+v", call.impersonation)
		}
	})

	t.Run("impersonation target and delegates change the client cache key", func(t *testing.T) {
		stubGKEWrap(t)
		b := NewIdentityAwareBuilder(nil)

		keyFor := func(isa *kconfig.ImpersonateServiceAccountConfig) string {
			pc := pcWithGoogleIdentity(t, xpv2.CredentialsSourceEnvironment, isa)
			key, err := b.ClientCacheKey(context.Background(), pc)
			if err != nil {
				t.Fatalf("ClientCacheKey(...): unexpected error: %v", err)
			}
			return key
		}

		none := keyFor(nil)
		target := keyFor(&kconfig.ImpersonateServiceAccountConfig{Name: "target@project.iam.gserviceaccount.com"})
		delegated := keyFor(&kconfig.ImpersonateServiceAccountConfig{
			Name:      "target@project.iam.gserviceaccount.com",
			Delegates: []string{"first@project.iam.gserviceaccount.com"},
		})

		if none == target {
			t.Fatal("expected the impersonation target to change the client cache key")
		}
		if target == delegated {
			t.Fatal("expected the delegation chain to change the client cache key")
		}
	})
}
