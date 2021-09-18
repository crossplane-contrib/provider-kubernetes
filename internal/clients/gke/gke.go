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
	"net/http"

	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"k8s.io/client-go/rest"
)

// DefaultScopes for GKE authentication.
var DefaultScopes []string = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
}

// WrapRESTConfig configures the supplied REST config to use OAuth2 bearer
// tokens fetched using the supplied Google Application Credentials.
func WrapRESTConfig(ctx context.Context, rc *rest.Config, credentials []byte, scopes ...string) error {
	creds, err := google.CredentialsFromJSON(ctx, credentials, scopes...)
	if err != nil {
		return errors.Wrap(err, "cannot load Google Application Credentials from JSON")
	}

	// CredentialsFromJSON creates a TokenSource that handles token caching.
	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &oauth2.Transport{Source: creds.TokenSource, Base: rt}
	})

	return nil
}
