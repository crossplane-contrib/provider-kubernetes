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
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd/api"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

func TestNewIdentityAwareBuilder(t *testing.T) {
	b := NewIdentityAwareBuilder(&test.MockClient{})

	if b == nil {
		t.Fatal("NewIdentityAwareBuilder returned nil")
	}
	if b.store == nil {
		t.Error("store should not be nil")
	}
	if b.cacheTTL != 30*time.Minute {
		t.Errorf("cacheTTL = %v, want %v", b.cacheTTL, 30*time.Minute)
	}
}

func TestCacheKeyForProviderConfig(t *testing.T) {
	tests := []struct {
		name string
		pc   kconfig.ProviderConfigSpec
		want string
	}{
		{
			name: "InjectedIdentity",
			pc: kconfig.ProviderConfigSpec{
				Credentials: kconfig.ProviderCredentials{
					Source: xpv1.CredentialsSourceInjectedIdentity,
				},
			},
			want: "injected-identity",
		},
		{
			name: "SecretRef",
			pc: kconfig.ProviderConfigSpec{
				Credentials: kconfig.ProviderCredentials{
					Source: xpv1.CredentialsSourceSecret,
					CommonCredentialSelectors: xpv1.CommonCredentialSelectors{
						SecretRef: &xpv1.SecretKeySelector{
							SecretReference: xpv1.SecretReference{
								Namespace: "default",
								Name:      "my-secret",
							},
						},
					},
				},
			},
			want: "secret:default/my-secret",
		},
		{
			name: "SecretRefDifferentNamespace",
			pc: kconfig.ProviderConfigSpec{
				Credentials: kconfig.ProviderCredentials{
					Source: xpv1.CredentialsSourceSecret,
					CommonCredentialSelectors: xpv1.CommonCredentialSelectors{
						SecretRef: &xpv1.SecretKeySelector{
							SecretReference: xpv1.SecretReference{
								Namespace: "prod",
								Name:      "cluster-creds",
							},
						},
					},
				},
			},
			want: "secret:prod/cluster-creds",
		},
		{
			name: "FallbackSource",
			pc: kconfig.ProviderConfigSpec{
				Credentials: kconfig.ProviderCredentials{
					Source: "SomeOtherSource",
				},
			},
			want: "source:SomeOtherSource",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewIdentityAwareBuilder(&test.MockClient{})
			got := b.cacheKeyForProviderConfig(tt.pc)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("cacheKeyForProviderConfig() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCacheKeyUniqueness(t *testing.T) {
	b := NewIdentityAwareBuilder(&test.MockClient{})

	// Two different secrets should have different cache keys
	pc1 := kconfig.ProviderConfigSpec{
		Credentials: kconfig.ProviderCredentials{
			Source: xpv1.CredentialsSourceSecret,
			CommonCredentialSelectors: xpv1.CommonCredentialSelectors{
				SecretRef: &xpv1.SecretKeySelector{
					SecretReference: xpv1.SecretReference{
						Namespace: "default",
						Name:      "secret-1",
					},
				},
			},
		},
	}

	pc2 := kconfig.ProviderConfigSpec{
		Credentials: kconfig.ProviderCredentials{
			Source: xpv1.CredentialsSourceSecret,
			CommonCredentialSelectors: xpv1.CommonCredentialSelectors{
				SecretRef: &xpv1.SecretKeySelector{
					SecretReference: xpv1.SecretReference{
						Namespace: "default",
						Name:      "secret-2",
					},
				},
			},
		},
	}

	key1 := b.cacheKeyForProviderConfig(pc1)
	key2 := b.cacheKeyForProviderConfig(pc2)

	if key1 == key2 {
		t.Errorf("different secrets should have different cache keys: %s == %s", key1, key2)
	}
}

func TestCachedClientExpiry(t *testing.T) {
	b := &IdentityAwareBuilder{
		cacheTTL: 100 * time.Millisecond, // Short TTL for testing
	}

	// Manually insert a cached client
	cacheKey := "test-key"
	mockClient := &cachedClient{
		client:     &test.MockClient{}, // We don't need a real client for this test
		restConfig: &rest.Config{Host: "https://cached-host"},
		createdAt:  time.Now().Add(-200 * time.Millisecond), // Created 200ms ago, already expired
	}
	b.cache.Store(cacheKey, mockClient)

	// Simulate what KubeForProviderConfig does when checking cache
	if cached, ok := b.cache.Load(cacheKey); ok {
		cc := cached.(*cachedClient)
		if time.Since(cc.createdAt) < b.cacheTTL {
			t.Error("expected client to be expired")
		}
		// Should be expired, remove it
		b.cache.Delete(cacheKey)
	}

	// Verify it was deleted
	if _, ok := b.cache.Load(cacheKey); ok {
		t.Error("expired client should have been removed from cache")
	}
}

func TestCacheHitWithValidClient(t *testing.T) {
	b := &IdentityAwareBuilder{
		cacheTTL: 30 * time.Minute,
	}

	// Manually insert a valid cached client
	cacheKey := "test-key"
	expectedConfig := &rest.Config{Host: "https://cached-host"}
	mockClient := &cachedClient{
		client:     nil, // In real scenario this would be a client
		restConfig: expectedConfig,
		createdAt:  time.Now(), // Just created, not expired
	}
	b.cache.Store(cacheKey, mockClient)

	// Simulate cache lookup
	if cached, ok := b.cache.Load(cacheKey); ok {
		cc := cached.(*cachedClient)
		if time.Since(cc.createdAt) >= b.cacheTTL {
			t.Error("client should not be expired")
		}
		if cc.restConfig.Host != expectedConfig.Host {
			t.Errorf("restConfig.Host = %s, want %s", cc.restConfig.Host, expectedConfig.Host)
		}
	} else {
		t.Error("expected cache hit")
	}
}

func TestBuilderFn(t *testing.T) {
	expectedClient := ctrlclient.Client(nil) // placeholder
	expectedConfig := &rest.Config{Host: "https://test-host"}
	called := false

	fn := BuilderFn(func(ctx context.Context, pc kconfig.ProviderConfigSpec) (ctrlclient.Client, *rest.Config, error) {
		called = true
		return expectedClient, expectedConfig, nil
	})

	// Verify it implements Builder interface
	var _ Builder = fn

	client, config, err := fn.KubeForProviderConfig(context.Background(), kconfig.ProviderConfigSpec{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("underlying function was not called")
	}
	if client != expectedClient {
		t.Error("returned client does not match expected")
	}
	if config.Host != expectedConfig.Host {
		t.Errorf("config.Host = %s, want %s", config.Host, expectedConfig.Host)
	}
}

func TestFromAPIConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      *api.Config
		wantHost    string
		wantErr     bool
		errContains string
	}{
		{
			name: "ValidConfig",
			config: &api.Config{
				CurrentContext: "test-context",
				Contexts: map[string]*api.Context{
					"test-context": {
						Cluster:  "test-cluster",
						AuthInfo: "test-user",
					},
				},
				Clusters: map[string]*api.Cluster{
					"test-cluster": {
						Server: "https://test-server:6443",
					},
				},
				AuthInfos: map[string]*api.AuthInfo{
					"test-user": {
						Token: "test-token",
					},
				},
			},
			wantHost: "https://test-server:6443",
			wantErr:  false,
		},
		{
			name: "MissingCurrentContext",
			config: &api.Config{
				CurrentContext: "",
			},
			wantErr:     true,
			errContains: "currentContext not set",
		},
		{
			name: "MissingCluster",
			config: &api.Config{
				CurrentContext: "test-context",
				Contexts: map[string]*api.Context{
					"test-context": {
						Cluster:  "nonexistent-cluster",
						AuthInfo: "test-user",
					},
				},
				Clusters: map[string]*api.Cluster{},
			},
			wantErr:     true,
			errContains: "cluster for currentContext",
		},
		{
			name: "MissingUserIsOK",
			config: &api.Config{
				CurrentContext: "test-context",
				Contexts: map[string]*api.Context{
					"test-context": {
						Cluster:  "test-cluster",
						AuthInfo: "nonexistent-user",
					},
				},
				Clusters: map[string]*api.Cluster{
					"test-cluster": {
						Server: "https://test-server:6443",
					},
				},
				AuthInfos: map[string]*api.AuthInfo{},
			},
			wantHost: "https://test-server:6443",
			wantErr:  false, // Missing user is OK per the code comment
		},
		{
			name: "WithTLSConfig",
			config: &api.Config{
				CurrentContext: "test-context",
				Contexts: map[string]*api.Context{
					"test-context": {
						Cluster:  "test-cluster",
						AuthInfo: "test-user",
					},
				},
				Clusters: map[string]*api.Cluster{
					"test-cluster": {
						Server:                   "https://test-server:6443",
						InsecureSkipTLSVerify:    true,
						CertificateAuthorityData: []byte("ca-data"),
					},
				},
				AuthInfos: map[string]*api.AuthInfo{
					"test-user": {
						ClientCertificateData: []byte("cert-data"),
						ClientKeyData:         []byte("key-data"),
					},
				},
			},
			wantHost: "https://test-server:6443",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fromAPIConfig(tt.config)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Host != tt.wantHost {
				t.Errorf("Host = %s, want %s", got.Host, tt.wantHost)
			}

			// Verify QPS and Burst are set
			if got.QPS != 50 {
				t.Errorf("QPS = %f, want 50", got.QPS)
			}
			if got.Burst != 300 {
				t.Errorf("Burst = %d, want 300", got.Burst)
			}
		})
	}
}

func TestFromAPIConfigWithImpersonation(t *testing.T) {
	config := &api.Config{
		CurrentContext: "test-context",
		Contexts: map[string]*api.Context{
			"test-context": {
				Cluster:  "test-cluster",
				AuthInfo: "test-user",
			},
		},
		Clusters: map[string]*api.Cluster{
			"test-cluster": {
				Server: "https://test-server:6443",
			},
		},
		AuthInfos: map[string]*api.AuthInfo{
			"test-user": {
				Impersonate:       "admin-user",
				ImpersonateGroups: []string{"system:masters"},
			},
		},
	}

	got, err := fromAPIConfig(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Impersonate.UserName != "admin-user" {
		t.Errorf("Impersonate.UserName = %s, want admin-user", got.Impersonate.UserName)
	}
	if len(got.Impersonate.Groups) != 1 || got.Impersonate.Groups[0] != "system:masters" {
		t.Errorf("Impersonate.Groups = %v, want [system:masters]", got.Impersonate.Groups)
	}
}

// helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
