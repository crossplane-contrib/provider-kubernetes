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

// Package gke contains utilities for authenticating to GKE clusters.
package gke

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"k8s.io/client-go/rest"
)

// DefaultScopes for GKE authentication.
var DefaultScopes []string = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
}

// Indirections over the Google auth helpers so that the token-source
// construction can be exercised in unit tests without reaching out to the
// metadata server or the IAM Credentials API.
var (
	defaultTokenSource         = google.DefaultTokenSource
	credentialsFromJSON        = google.CredentialsFromJSON
	newImpersonatedTokenSource = impersonate.CredentialsTokenSource
)

// Impersonation configures optional GCP service account impersonation.
type Impersonation struct {
	// TargetPrincipal is the email address of the service account to
	// impersonate.
	TargetPrincipal string

	// Delegates is an optional delegation chain of service account email
	// addresses used to reach TargetPrincipal. Each service account must be
	// granted roles/iam.serviceAccountTokenCreator on the next one in the
	// chain, with the last delegate having it on TargetPrincipal.
	Delegates []string
}

// WrapRESTConfig configures the supplied REST config to use OAuth2 bearer
// tokens fetched using the supplied Google Application Credentials.
//
// When impersonation is non-nil and its TargetPrincipal is set, the base
// credentials built from the supplied credentials (or the injected identity)
// are used to impersonate the target service account (optionally through a
// delegation chain), and the impersonated token source is what ultimately
// signs requests.
func WrapRESTConfig(ctx context.Context, rc *rest.Config, credentials []byte, impersonation *Impersonation, scopes ...string) error {
	// TODO(turkenh): Use token.ReuseSourceStore to cache token sources and
	// avoid token regeneration on every reconciliation loop.
	var ts oauth2.TokenSource
	switch {
	case credentials == nil:
		// DefaultTokenSource retrieves a token source from an injected identity.
		gsrc, err := defaultTokenSource(ctx, scopes...)
		if err != nil {
			return errors.Wrap(err, "failed to extract default credentials source")
		}
		ts = oauth2.ReuseTokenSource(nil, gsrc)
	case isJSON(credentials):
		// If credentials are in a JSON format, extract the credential from the JSON.
		// CredentialsFromJSON creates a TokenSource that handles token caching.
		creds, err := credentialsFromJSON(ctx, credentials, scopes...)
		if err != nil {
			return errors.Wrap(err, "cannot load Google Application Credentials from JSON")
		}
		ts = creds.TokenSource
	default:
		// If the credential is not in a JSON format, treat it as an access token.
		t := oauth2.Token{
			AccessToken: string(credentials),
		}
		if ok := t.Valid(); !ok {
			return errors.New("access token invalid")
		}
		ts = oauth2.StaticTokenSource(&t)
	}

	if impersonation != nil && impersonation.TargetPrincipal != "" {
		// Exchange the base credentials for a token that impersonates the
		// target service account. The base token source is passed explicitly so
		// that the configured credentials sign the impersonation request rather
		// than falling back to Application Default Credentials.
		its, err := newImpersonatedTokenSource(ctx,
			impersonate.CredentialsConfig{
				TargetPrincipal: impersonation.TargetPrincipal,
				Scopes:          scopes,
				Delegates:       impersonation.Delegates,
			},
			option.WithTokenSource(ts),
		)
		if err != nil {
			return errors.Wrap(err, "cannot create impersonated token source")
		}
		ts = its
	}

	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &oauth2.Transport{Source: ts, Base: rt}
	})

	return nil
}

func isJSON(b []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(b, &js) == nil
}
