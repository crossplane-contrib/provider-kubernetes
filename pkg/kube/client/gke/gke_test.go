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

package gke

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"k8s.io/client-go/rest"
)

// staticSource is a trivial oauth2.TokenSource used to stub out the network
// dependent Google auth helpers in tests.
func staticSource(token string) oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
}

// stubHelpers replaces the package level Google auth indirections for the
// duration of a test and restores them via t.Cleanup.
func stubHelpers(t *testing.T,
	def func(context.Context, ...string) (oauth2.TokenSource, error),
	fromJSON func(context.Context, []byte, ...string) (*google.Credentials, error),
	imp func(context.Context, impersonate.CredentialsConfig, ...option.ClientOption) (oauth2.TokenSource, error),
) {
	t.Helper()
	origDef, origJSON, origImp := defaultTokenSource, credentialsFromJSON, newImpersonatedTokenSource
	if def != nil {
		defaultTokenSource = def
	}
	if fromJSON != nil {
		credentialsFromJSON = fromJSON
	}
	if imp != nil {
		newImpersonatedTokenSource = imp
	}
	t.Cleanup(func() {
		defaultTokenSource = origDef
		credentialsFromJSON = origJSON
		newImpersonatedTokenSource = origImp
	})
}

// transportSource extracts the oauth2 token source that WrapRESTConfig wired
// into the REST config's transport chain.
func transportSource(t *testing.T, rc *rest.Config) oauth2.TokenSource {
	t.Helper()
	if rc.WrapTransport == nil {
		t.Fatal("WrapTransport was not set")
	}
	rt := rc.WrapTransport(http.DefaultTransport)
	ot, ok := rt.(*oauth2.Transport)
	if !ok {
		t.Fatalf("expected *oauth2.Transport, got %T", rt)
	}
	return ot.Source
}

func TestWrapRESTConfigAccessToken(t *testing.T) {
	rc := &rest.Config{}
	if err := WrapRESTConfig(context.Background(), rc, []byte("ya29.some-access-token"), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src := transportSource(t, rc); src == nil {
		t.Fatal("expected a non-nil token source on the transport")
	}
}

func TestWrapRESTConfigInvalidAccessToken(t *testing.T) {
	rc := &rest.Config{}
	err := WrapRESTConfig(context.Background(), rc, []byte(""), nil)
	if err == nil {
		t.Fatal("expected an error for an empty/invalid access token")
	}
	if !strings.Contains(err.Error(), "access token invalid") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWrapRESTConfigInjectedIdentity(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		stubHelpers(t, func(_ context.Context, _ ...string) (oauth2.TokenSource, error) {
			return staticSource("injected"), nil
		}, nil, nil)

		rc := &rest.Config{}
		if err := WrapRESTConfig(context.Background(), rc, nil, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src := transportSource(t, rc); src == nil {
			t.Fatal("expected a non-nil token source on the transport")
		}
	})

	t.Run("failure is wrapped", func(t *testing.T) {
		stubHelpers(t, func(_ context.Context, _ ...string) (oauth2.TokenSource, error) {
			return nil, errors.New("boom")
		}, nil, nil)

		rc := &rest.Config{}
		err := WrapRESTConfig(context.Background(), rc, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "failed to extract default credentials source") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestWrapRESTConfigJSONCredentials(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		stubHelpers(t, nil, func(_ context.Context, _ []byte, _ ...string) (*google.Credentials, error) {
			return &google.Credentials{TokenSource: staticSource("from-json")}, nil
		}, nil)

		rc := &rest.Config{}
		if err := WrapRESTConfig(context.Background(), rc, []byte(`{"type":"service_account"}`), nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src := transportSource(t, rc); src == nil {
			t.Fatal("expected a non-nil token source on the transport")
		}
	})

	t.Run("failure is wrapped", func(t *testing.T) {
		stubHelpers(t, nil, func(_ context.Context, _ []byte, _ ...string) (*google.Credentials, error) {
			return nil, errors.New("bad json")
		}, nil)

		rc := &rest.Config{}
		err := WrapRESTConfig(context.Background(), rc, []byte(`{"type":"service_account"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "cannot load Google Application Credentials from JSON") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestWrapRESTConfigImpersonation(t *testing.T) {
	t.Run("not requested when nil", func(t *testing.T) {
		called := false
		stubHelpers(t, nil, nil, func(_ context.Context, _ impersonate.CredentialsConfig, _ ...option.ClientOption) (oauth2.TokenSource, error) {
			called = true
			return staticSource("impersonated"), nil
		})

		rc := &rest.Config{}
		if err := WrapRESTConfig(context.Background(), rc, []byte("ya29.token"), nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if called {
			t.Fatal("impersonation must not be attempted when no service account is configured")
		}
	})

	t.Run("not requested when target principal is empty", func(t *testing.T) {
		called := false
		stubHelpers(t, nil, nil, func(_ context.Context, _ impersonate.CredentialsConfig, _ ...option.ClientOption) (oauth2.TokenSource, error) {
			called = true
			return staticSource("impersonated"), nil
		})

		rc := &rest.Config{}
		if err := WrapRESTConfig(context.Background(), rc, []byte("ya29.token"), &Impersonation{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if called {
			t.Fatal("impersonation must not be attempted with an empty target principal")
		}
	})

	t.Run("uses configured base credentials as source", func(t *testing.T) {
		const sa = "target@project.iam.gserviceaccount.com"
		var (
			gotConfig impersonate.CredentialsConfig
			gotOpts   []option.ClientOption
		)
		stubHelpers(t, nil, nil, func(_ context.Context, cfg impersonate.CredentialsConfig, opts ...option.ClientOption) (oauth2.TokenSource, error) {
			gotConfig = cfg
			gotOpts = opts
			return staticSource("impersonated"), nil
		})

		rc := &rest.Config{}
		if err := WrapRESTConfig(context.Background(), rc, []byte("ya29.token"), &Impersonation{TargetPrincipal: sa}, DefaultScopes...); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotConfig.TargetPrincipal != sa {
			t.Fatalf("unexpected target principal: %q", gotConfig.TargetPrincipal)
		}
		if len(gotConfig.Scopes) != len(DefaultScopes) {
			t.Fatalf("expected scopes %v to be forwarded, got %v", DefaultScopes, gotConfig.Scopes)
		}
		// The regression guard for the original bug: the base token source
		// built from the supplied credentials must be threaded through as a
		// client option so that impersonation is signed by the configured
		// credentials rather than Application Default Credentials.
		if len(gotOpts) == 0 {
			t.Fatal("expected the base token source to be passed as a client option")
		}
		if src := transportSource(t, rc); src == nil {
			t.Fatal("expected a non-nil token source on the transport")
		}
	})

	t.Run("forwards the delegation chain", func(t *testing.T) {
		const sa = "target@project.iam.gserviceaccount.com"
		delegates := []string{
			"first@project.iam.gserviceaccount.com",
			"second@project.iam.gserviceaccount.com",
		}
		var gotConfig impersonate.CredentialsConfig
		stubHelpers(t, nil, nil, func(_ context.Context, cfg impersonate.CredentialsConfig, _ ...option.ClientOption) (oauth2.TokenSource, error) {
			gotConfig = cfg
			return staticSource("impersonated"), nil
		})

		rc := &rest.Config{}
		if err := WrapRESTConfig(context.Background(), rc, []byte("ya29.token"), &Impersonation{TargetPrincipal: sa, Delegates: delegates}, DefaultScopes...); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if diff := cmp.Diff(delegates, gotConfig.Delegates); diff != "" {
			t.Fatalf("delegation chain not forwarded, -want +got:\n%s", diff)
		}
	})

	t.Run("failure is wrapped", func(t *testing.T) {
		stubHelpers(t, nil, nil, func(_ context.Context, _ impersonate.CredentialsConfig, _ ...option.ClientOption) (oauth2.TokenSource, error) {
			return nil, errors.New("iam denied")
		})

		rc := &rest.Config{}
		err := WrapRESTConfig(context.Background(), rc, []byte("ya29.token"), &Impersonation{TargetPrincipal: "target@project.iam.gserviceaccount.com"})
		if err == nil || !strings.Contains(err.Error(), "cannot create impersonated token source") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
