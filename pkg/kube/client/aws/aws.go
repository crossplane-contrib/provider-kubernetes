/*
Copyright 2025 The Crossplane Authors.
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

// Package aws contains utilities for authenticating to EKS clusters.
package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/pkg/errors"
	"k8s.io/client-go/rest"
)

const (
	// clusterIDHeader is the header name for the cluster ID
	clusterIDHeader = "x-k8s-aws-id"
	// expireHeader is the header name for the expiration time
	expireHeader = "X-Amz-Expires"
	// tokenPrefix is the prefix for the EKS token
	tokenPrefix = "k8s-aws-v1."
	// tokenExpiration is the default expiration time for EKS tokens (15 minutes)
	tokenExpiration = 900
)

// WrapRESTConfig configures the supplied REST config to use bearer tokens
// fetched using AWS credentials chain for EKS authentication.
// This uses AWS Web Identity / IRSA to assume a role that has access to the EKS cluster.
// clusterNameFromKubeconfig is the cluster name from the kubeconfig (can be an ARN or plain name).
func WrapRESTConfig(ctx context.Context, rc *rest.Config, clusterNameFromKubeconfig string) error {
	// Extract cluster name from ARN if needed
	clusterName, err := extractClusterNameFromARN(clusterNameFromKubeconfig)
	if err != nil {
		return errors.Wrap(err, "failed to extract cluster name from kubeconfig")
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to load AWS config using default credentials chain")
	}

	// Create STS client with the credentials (which may already be assumed role credentials)
	stsClient := sts.NewFromConfig(cfg)

	// Verify credentials by getting caller identity
	_, err = stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return errors.Wrap(err, "failed to verify AWS credentials with GetCallerIdentity")
	}

	// Create a token source that generates EKS tokens on demand
	tokenSource := &eksTokenSource{
		ctx:       ctx,
		stsClient: stsClient,
		clusterID: clusterName,
	}

	// Wrap the transport to inject the bearer token
	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &bearerAuthRoundTripper{
			source: tokenSource,
			rt:     rt,
		}
	})

	// Clear any exec provider since we're handling auth ourselves
	rc.ExecProvider = nil

	return nil
}

// extractClusterNameFromARN extracts the cluster name from an EKS cluster ARN
// ARN format: arn:aws:eks:region:account:cluster/cluster-name
func extractClusterNameFromARN(arnString string) (string, error) {
	// Check if it's an ARN using AWS SDK
	if !arn.IsARN(arnString) {
		// Not an ARN, might be just the cluster name
		return arnString, nil
	}

	// Parse ARN using AWS SDK
	parsedARN, err := arn.Parse(arnString)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse ARN")
	}

	// EKS cluster ARNs have Resource in format "cluster/cluster-name"
	// Split by '/' to get the cluster name
	parts := strings.Split(parsedARN.Resource, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid EKS cluster ARN resource format: %s", parsedARN.Resource)
	}

	return parts[len(parts)-1], nil
}

// eksTokenSource generates EKS authentication tokens using AWS STS
type eksTokenSource struct {
	ctx       context.Context
	stsClient *sts.Client
	clusterID string
}

// Token generates an EKS authentication token
// This replicates the behavior of `aws eks get-token` command
// The STS client uses credentials from the AWS default credentials chain,
// which includes assumed role credentials from Web Identity/IRSA
func (s *eksTokenSource) Token() (string, error) {
	// Create a presigned request for GetCallerIdentity
	// This is what EKS uses for authentication
	// Default expiration is 15 minutes (900 seconds) which is what EKS expects
	presigner := sts.NewPresignClient(s.stsClient)

	// Create presigned request with cluster ID and expiration headers
	// This matches the provider-aws implementation exactly
	presignedReq, err := presigner.PresignGetCallerIdentity(s.ctx,
		&sts.GetCallerIdentityInput{},
		func(po *sts.PresignOptions) {
			po.ClientOptions = []func(*sts.Options){
				sts.WithAPIOptions(
					smithyhttp.AddHeaderValue(clusterIDHeader, s.clusterID),
					smithyhttp.AddHeaderValue(expireHeader, fmt.Sprintf("%d", tokenExpiration)),
				),
			}
		})
	if err != nil {
		return "", errors.Wrap(err, "failed to presign GetCallerIdentity request")
	}

	// Encode the presigned URL as a base64 token with the EKS prefix
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(presignedReq.URL))

	return token, nil
}

// bearerAuthRoundTripper injects a bearer token into HTTP requests
type bearerAuthRoundTripper struct {
	source *eksTokenSource
	rt     http.RoundTripper
}

// RoundTrip implements http.RoundTripper
func (b *bearerAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := b.source.Token()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get EKS token")
	}

	// Clone the request and add the bearer token
	reqCopy := req.Clone(req.Context())
	reqCopy.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	return b.rt.RoundTrip(reqCopy)
}
