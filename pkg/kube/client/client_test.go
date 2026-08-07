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
	"net/http"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
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
