/*
Copyright 2017 The Kubernetes Authors.

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

/*
This file is forked from upstream kubernetes/client-go
https://github.com/kubernetes/client-go/blob/0b9a7d2f21befcfd98bf2e62ae68ea49d682500d/openapi/groupversion.go
*/

package ssa

import (
	"context"
	"net/url"

	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/handler3"
)

// OpenAPIGroupVersion is the context-aware variant of openapi.GroupVersion
// It also stores the ETag for the underlying discovery GV path
type OpenAPIGroupVersion interface {
	// Schema is the context-accepting variant of the upstream client-go implementation
	Schema(ctx context.Context, contentType string) ([]byte, error)
	ETag() string
}

// customOAPIGroupVersion is the customized variant of the unexported
// openapi.groupversion with ETag extracted.
// see https://github.com/kubernetes/client-go/blob/78c1586020d8bef4d031a556f867544ca34845a1/openapi/groupversion.go#L32
type customOAPIGroupVersion struct {
	restClient      rest.Interface
	item            handler3.OpenAPIV3DiscoveryGroupVersion
	useClientPrefix bool
	etag            string
}

// newCustomOAPIGroupVersion returns a new customOAPIGroupVersion instance
func newCustomOAPIGroupVersion(client rest.Interface, item handler3.OpenAPIV3DiscoveryGroupVersion, useClientPrefix bool, etag string) *customOAPIGroupVersion {
	return &customOAPIGroupVersion{restClient: client, item: item, useClientPrefix: useClientPrefix, etag: etag}
}

// Schema returns the OpenAPI schema for the OpenAPI GroupVersion with the given context
// adapted from
// https://github.com/kubernetes/client-go/blob/78c1586020d8bef4d031a556f867544ca34845a1/openapi/groupversion.go#L42
func (g *customOAPIGroupVersion) Schema(ctx context.Context, contentType string) ([]byte, error) {
	if !g.useClientPrefix {
		return g.restClient.Get().
			RequestURI(g.item.ServerRelativeURL).
			SetHeader("Accept", contentType).
			Do(ctx).
			Raw()
	}

	locator, err := url.Parse(g.item.ServerRelativeURL)
	if err != nil {
		return nil, err
	}

	path := g.restClient.Get().
		AbsPath(locator.Path).
		SetHeader("Accept", contentType)

	// Other than root endpoints(openapiv3/apis), resources have hash query parameter to support etags.
	// However, absPath does not support handling query parameters internally,
	// so that hash query parameter is added manually
	for k, value := range locator.Query() {
		for _, v := range value {
			path.Param(k, v)
		}
	}

	return path.Do(ctx).Raw()
}

// ETag returns the associated ETag for the schema document
func (g *customOAPIGroupVersion) ETag() string {
	return g.etag
}
