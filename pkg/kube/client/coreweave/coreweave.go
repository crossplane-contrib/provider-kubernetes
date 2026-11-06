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

// Package coreweave provides authentication to CoreWeave Kubernetes Service
// (CKS) clusters using a CoreWeave API access token.
package coreweave

import (
	"encoding/json"
	"strings"

	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// credentials mirrors the CoreWeave credentials JSON, matching the format
// used by provider-coreweave.
type credentials struct {
	Token string `json:"token"`
}

// parseToken unmarshals the CoreWeave credentials JSON held in the identity
// Secret and returns its API access token.
func parseToken(creds []byte) (string, error) {
	var c credentials
	if err := json.Unmarshal(creds, &c); err != nil {
		return "", errors.Wrap(err, "cannot parse CoreWeave credentials")
	}
	t := strings.TrimSpace(c.Token)
	if t == "" {
		return "", errors.New("CoreWeave credentials must contain a non-empty token")
	}
	return t, nil
}

// WrapRESTConfig configures rc to authenticate to a CKS cluster using the
// CoreWeave API access token in the supplied credentials. CKS accepts the
// token as a bearer token as is, so no exchange or refresh is needed. Any
// other user authentication configured by the kubeconfig is cleared.
func WrapRESTConfig(rc *rest.Config, creds []byte) error {
	t, err := parseToken(creds)
	if err != nil {
		return err
	}

	rc.BearerToken = t
	rc.BearerTokenFile = ""
	rc.Username = ""
	rc.Password = ""
	rc.ExecProvider = nil
	rc.AuthProvider = nil
	return nil
}
