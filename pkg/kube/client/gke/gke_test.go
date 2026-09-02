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
	"k8s.io/client-go/rest"
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
			if err := WrapRESTConfig(ctx, rc, externalAccountCredentials(t, tokenServer.URL), DefaultScopes...); err != nil {
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
