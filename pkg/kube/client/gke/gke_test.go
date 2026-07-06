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

package gke

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// externalAccountCredentials returns workload identity federation credentials
// that exchange a file-sourced subject token at tokenURL on every refresh,
// using the context the token source was created with.
func externalAccountCredentials(t *testing.T, tokenURL string) []byte {
	t.Helper()
	subjectTokenFile := filepath.Join(t.TempDir(), "subject-token")
	if err := os.WriteFile(subjectTokenFile, []byte("subject-token"), 0o600); err != nil {
		t.Fatalf("WriteFile(...): unexpected error: %v", err)
	}
	return fmt.Appendf(nil, `{
  "type": "external_account",
  "audience": "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/pool/providers/provider",
  "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
  "token_url": %q,
  "credential_source": {"file": %q}
}`, tokenURL, subjectTokenFile)
}

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

func TestWithTokenFetchClient(t *testing.T) {
	callerClient := &http.Client{Timeout: time.Second}

	type args struct {
		ctx context.Context
	}
	type want struct {
		client *http.Client
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"DefaultClientInstalled": {
			args: args{ctx: context.Background()},
			want: want{client: &http.Client{Timeout: tokenFetchTimeout}},
		},
		"CallerClientKept": {
			args: args{ctx: context.WithValue(context.Background(), oauth2.HTTPClient, callerClient)},
			want: want{client: callerClient},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, _ := withTokenFetchClient(tc.args.ctx).Value(oauth2.HTTPClient).(*http.Client)
			if diff := cmp.Diff(tc.want.client, got); diff != "" {
				t.Errorf("withTokenFetchClient(...): -want client, +got client:\n%s", diff)
			}
		})
	}
}

// TestWrapRESTConfigTokenFetch exercises the token exchange behind the wrapped
// transport against an httptest token endpoint: the exchanged token must
// authenticate requests, and a token endpoint that never answers must fail
// the request within the timeout of the HTTP client carried by the context
// instead of blocking it for good.
func TestWrapRESTConfigTokenFetch(t *testing.T) {
	type result struct {
		Authorization string
		// TimedOut reports whether the token exchange ended by the HTTP
		// client's timeout. The oauth2 library wraps that error without %w,
		// so it is recognized by net/http's message rather than by type.
		TimedOut bool
	}
	type args struct {
		// tokenTimeout replaces the wrapper's default timeout when set, so
		// the silent endpoint case does not have to wait for it.
		tokenTimeout time.Duration
		tokenServer  http.HandlerFunc
	}
	type want struct {
		result result
		err    bool
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"ExchangedTokenAuthenticatesRequest": {
			args: args{
				tokenServer: func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"access_token":"sts-token","token_type":"Bearer","expires_in":3600}`))
				},
			},
			want: want{result: result{Authorization: "Bearer sts-token"}},
		},
		"SilentTokenEndpointFailsWithinTimeout": {
			args: args{
				tokenTimeout: 200 * time.Millisecond,
				tokenServer: func(_ http.ResponseWriter, r *http.Request) {
					// The server only notices the client giving up once the
					// request body has been consumed.
					_, _ = io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
				},
			},
			want: want{result: result{TimedOut: true}, err: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tokenServer := httptest.NewServer(tc.args.tokenServer)
			defer tokenServer.Close()

			var authorization atomic.Pointer[string]
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h := r.Header.Get("Authorization")
				authorization.Store(&h)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer apiServer.Close()

			ctx := context.Background()
			if tc.args.tokenTimeout > 0 {
				ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: tc.args.tokenTimeout})
			}
			rc := &rest.Config{Host: apiServer.URL}
			if err := WrapRESTConfig(ctx, rc, externalAccountCredentials(t, tokenServer.URL), nil, DefaultScopes...); err != nil {
				t.Fatalf("WrapRESTConfig(...): unexpected error: %v", err)
			}
			hc, err := rest.HTTPClientFor(rc)
			if err != nil {
				t.Fatalf("rest.HTTPClientFor(...): unexpected error: %v", err)
			}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiServer.URL+"/version", nil)
			if err != nil {
				t.Fatalf("http.NewRequestWithContext(...): unexpected error: %v", err)
			}

			resp, err := hc.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}

			got := result{}
			if h := authorization.Load(); h != nil {
				got.Authorization = *h
			}
			got.TimedOut = err != nil && strings.Contains(err.Error(), "Client.Timeout exceeded")
			if diff := cmp.Diff(tc.want.err, err != nil); diff != "" {
				t.Errorf("request through the wrapped REST config: -want error, +got error: %v\n%s", err, diff)
			}
			if diff := cmp.Diff(tc.want.result, got); diff != "" {
				t.Errorf("request through the wrapped REST config (error: %v): -want, +got:\n%s", err, diff)
			}
		})
	}
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
