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

package coreweave

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestWrapRESTConfig(t *testing.T) {
	type want struct {
		rc  *rest.Config
		err bool
	}
	cases := map[string]struct {
		reason string
		rc     *rest.Config
		creds  string
		want   want
	}{
		"ValidCredentials": {
			reason: "The token should be set as the bearer token.",
			rc:     &rest.Config{Host: "https://cks.example.org"},
			creds:  `{"token":"CW-SECRET-abc"}`,
			want: want{
				rc: &rest.Config{Host: "https://cks.example.org", BearerToken: "CW-SECRET-abc"},
			},
		},
		"TokenWithWhitespace": {
			reason: "Whitespace around the token should be trimmed.",
			rc:     &rest.Config{Host: "https://cks.example.org"},
			creds:  "{\"token\":\" CW-SECRET-abc\\n\"}\n",
			want: want{
				rc: &rest.Config{Host: "https://cks.example.org", BearerToken: "CW-SECRET-abc"},
			},
		},
		"OtherAuthCleared": {
			reason: "Any other user authentication from the kubeconfig should be cleared.",
			rc: &rest.Config{
				Host:            "https://cks.example.org",
				BearerToken:     "old",
				BearerTokenFile: "/var/run/token",
				Username:        "user",
				Password:        "pass",
				ExecProvider:    &clientcmdapi.ExecConfig{Command: "cw"},
				AuthProvider:    &clientcmdapi.AuthProviderConfig{Name: "oidc"},
			},
			creds: `{"token":"CW-SECRET-abc"}`,
			want: want{
				rc: &rest.Config{Host: "https://cks.example.org", BearerToken: "CW-SECRET-abc"},
			},
		},
		"InvalidJSON": {
			reason: "Credentials that are not JSON should be rejected.",
			rc:     &rest.Config{Host: "https://cks.example.org"},
			creds:  "not json",
			want: want{
				rc:  &rest.Config{Host: "https://cks.example.org"},
				err: true,
			},
		},
		"EmptyToken": {
			reason: "Credentials with an empty token should be rejected.",
			rc:     &rest.Config{Host: "https://cks.example.org"},
			creds:  `{"token":"  "}`,
			want: want{
				rc:  &rest.Config{Host: "https://cks.example.org"},
				err: true,
			},
		},
		"MissingToken": {
			reason: "Credentials without a token should be rejected.",
			rc:     &rest.Config{Host: "https://cks.example.org"},
			creds:  `{}`,
			want: want{
				rc:  &rest.Config{Host: "https://cks.example.org"},
				err: true,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := WrapRESTConfig(tc.rc, []byte(tc.creds))
			if diff := cmp.Diff(tc.want.err, err != nil); diff != "" {
				t.Errorf("\n%s\nWrapRESTConfig(...): -want error, +got error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.rc, tc.rc); diff != "" {
				t.Errorf("\n%s\nWrapRESTConfig(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
