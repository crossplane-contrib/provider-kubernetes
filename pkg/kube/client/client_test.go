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

package client

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/gke"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

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

// gkeWrapCall records the arguments restForProviderConfig passed to the GKE
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

func TestRestForProviderConfigGoogleIdentityWiring(t *testing.T) {
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
		if _, err := b.restForProviderConfig(context.Background(), pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

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
		if _, err := b.restForProviderConfig(context.Background(), pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
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
		if _, err := b.restForProviderConfig(context.Background(), pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if call.credentials != nil {
			t.Fatalf("expected nil credentials on the injected identity path, got %q", call.credentials)
		}
		if call.impersonation == nil || call.impersonation.TargetPrincipal != "target@project.iam.gserviceaccount.com" {
			t.Fatalf("impersonation not mapped on the injected identity path: %+v", call.impersonation)
		}
	})
}
