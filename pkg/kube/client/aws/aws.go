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

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/pkg/errors"
	"k8s.io/client-go/rest"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
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
// This uses the AWS default credentials chain, including Web Identity / IRSA.
// clusterNameFromKubeconfig is the cluster name from the kubeconfig (can be an ARN or plain name).
// roleChain, when supplied, is assumed in order before EKS tokens are signed.
func WrapRESTConfig(ctx context.Context, rc *rest.Config, clusterNameFromKubeconfig string, roleChain ...kconfig.AWSAssumeRoleOptions) error {
	clusterName, region, err := ClusterNameAndRegion(clusterNameFromKubeconfig)
	if err != nil {
		return errors.Wrap(err, "failed to extract cluster name from kubeconfig")
	}

	loadOptions := make([]func(*config.LoadOptions) error, 0, 1)
	if region != "" {
		loadOptions = append(loadOptions, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return errors.Wrap(err, "failed to load AWS config using default credentials chain")
	}

	for i, role := range roleChain {
		stsClient := sts.NewFromConfig(cfg)
		provider := stscreds.NewAssumeRoleProvider(stsClient, role.RoleARN, func(o *stscreds.AssumeRoleOptions) {
			o.ExternalID = role.ExternalID
			o.TransitiveTagKeys = append([]string(nil), role.TransitiveTagKeys...)
			o.Tags = make([]types.Tag, 0, len(role.Tags))
			for _, tag := range role.Tags {
				o.Tags = append(o.Tags, types.Tag{Key: awssdk.String(tag.Key), Value: awssdk.String(tag.Value)})
			}
		})
		cfg.Credentials = awssdk.NewCredentialsCache(&assumeRoleCredentialsProvider{
			CredentialsProvider: provider,
			roleARN:             role.RoleARN,
			hop:                 i + 1,
		})
	}

	// Create a token source that generates EKS tokens on demand
	tokenSource := &eksTokenSource{
		stsClient: sts.NewFromConfig(cfg),
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

// ClusterNameAndRegion returns an EKS cluster name and any region specified by
// an EKS cluster ARN. Plain cluster names have no associated region.
// ARN format: arn:aws:eks:region:account:cluster/cluster-name
func ClusterNameAndRegion(arnString string) (string, string, error) {
	// Check if it's an ARN using AWS SDK
	if !arn.IsARN(arnString) {
		// Not an ARN, might be just the cluster name
		return arnString, "", nil
	}

	// Parse ARN using AWS SDK
	parsedARN, err := arn.Parse(arnString)
	if err != nil {
		return "", "", errors.Wrap(err, "failed to parse ARN")
	}
	if parsedARN.Service != "eks" {
		return "", "", fmt.Errorf("ARN is for service %q, not EKS", parsedARN.Service)
	}

	clusterName, ok := strings.CutPrefix(parsedARN.Resource, "cluster/")
	if !ok || clusterName == "" || strings.Contains(clusterName, "/") {
		return "", "", fmt.Errorf("invalid EKS cluster ARN resource format: %s", parsedARN.Resource)
	}

	return clusterName, parsedARN.Region, nil
}

type assumeRoleCredentialsProvider struct {
	awssdk.CredentialsProvider
	roleARN string
	hop     int
}

func (p *assumeRoleCredentialsProvider) Retrieve(ctx context.Context) (awssdk.Credentials, error) {
	credentials, err := p.CredentialsProvider.Retrieve(ctx)
	if err != nil {
		return awssdk.Credentials{}, errors.Wrapf(err, "failed to assume IAM role %q at chain hop %d", p.roleARN, p.hop)
	}
	return credentials, nil
}

// tokenSource issues an EKS bearer token for the request carrying ctx.
type tokenSource interface {
	Token(ctx context.Context) (string, error)
}

// eksTokenSource generates EKS authentication tokens using AWS STS
type eksTokenSource struct {
	stsClient *sts.Client
	clusterID string
}

// Token generates an EKS authentication token
// This replicates the behavior of `aws eks get-token` command
// The STS client uses credentials from the AWS default credentials chain,
// which includes assumed role credentials from Web Identity/IRSA.
// ctx is the context of the request being authenticated: the wrapped REST
// config outlives the reconcile that built it (cached clients, long-lived
// informers), so the token source must not hold on to a context of its own.
func (s *eksTokenSource) Token(ctx context.Context) (string, error) {
	// Create a presigned request for GetCallerIdentity
	// This is what EKS uses for authentication
	// Default expiration is 15 minutes (900 seconds) which is what EKS expects
	presigner := sts.NewPresignClient(s.stsClient)

	// Create presigned request with cluster ID and expiration headers
	// This matches the provider-aws implementation exactly
	presignedReq, err := presigner.PresignGetCallerIdentity(ctx,
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
	source tokenSource
	rt     http.RoundTripper
}

// RoundTrip implements http.RoundTripper
func (b *bearerAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := b.source.Token(req.Context())
	if err != nil {
		return nil, errors.Wrap(err, "failed to get EKS token")
	}

	// Clone the request and add the bearer token
	reqCopy := req.Clone(req.Context())
	reqCopy.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	return b.rt.RoundTrip(reqCopy)
}
