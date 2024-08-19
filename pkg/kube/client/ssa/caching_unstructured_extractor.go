// SPDX-FileCopyrightText: 2024 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package ssa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/kube-openapi/pkg/handler3"
	"k8s.io/kube-openapi/pkg/schemaconv"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	smdschema "sigs.k8s.io/structured-merge-diff/v4/schema"
	"sigs.k8s.io/structured-merge-diff/v4/typed"
)

// cachingUnstructuredExtractor is a caching implementation of v1.UnstructuredExtractor
// using OpenAPI V3 discovery information.
// TODO(erhan): try to upstream this code in kubernetes
type cachingUnstructuredExtractor struct {
	// added as field to not break the interface for other funcs, instantiated at each reconcile
	ctx   context.Context
	cache *GvkParserCache
	dc    discovery.DiscoveryInterface
}

// NewCachingUnstructuredExtractor returns a new cachingUnstructuredExtractor
func NewCachingUnstructuredExtractor(ctx context.Context, dc discovery.DiscoveryInterface, cache *GvkParserCache) (applymetav1.UnstructuredExtractor, error) {
	return &cachingUnstructuredExtractor{
		dc:    dc,
		cache: cache,
		ctx:   ctx,
	}, nil
}

// Extract extracts the applied configuration owned by fieldManager from an unstructured object.
// Note that the apply configuration itself is also an unstructured object.
func (e *cachingUnstructuredExtractor) Extract(object *unstructured.Unstructured, fieldManager string) (*unstructured.Unstructured, error) {
	return e.extractUnstructured(object, fieldManager, "")
}

// ExtractStatus is the same as ExtractUnstructured except
// that it extracts the status subresource applied configuration.
// Experimental!
func (e *cachingUnstructuredExtractor) ExtractStatus(object *unstructured.Unstructured, fieldManager string) (*unstructured.Unstructured, error) {
	return e.extractUnstructured(object, fieldManager, "status")
}

// getParserForGV fetches the *GVKParser for the given GVK.
func (e *cachingUnstructuredExtractor) getParserForGV(ctx context.Context, gv schema.GroupVersion) (*GvkParser, error) {
	data, err := e.dc.RESTClient().Get().
		AbsPath("/openapi/v3").
		Do(ctx).
		Raw()

	if err != nil {
		return nil, err
	}

	discoMap := &handler3.OpenAPIV3Discovery{}
	err = json.Unmarshal(data, discoMap)
	if err != nil {
		return nil, err
	}
	// parse discovery information
	oapiPathsToGV := map[string]OpenAPIGroupVersion{}
	for path, oapiGV := range discoMap.Paths {
		parse, err := url.Parse(oapiGV.ServerRelativeURL)
		if err != nil {
			return nil, err
		}
		useClientPrefix := strings.HasPrefix(oapiGV.ServerRelativeURL, "/openapi/v3")
		etag := parse.Query().Get("hash")
		oapiPathsToGV[path] = newCustomOAPIGroupVersion(e.dc.RESTClient(), oapiGV, useClientPrefix, etag)
	}

	e.cache.mu.Lock()
	defer e.cache.mu.Unlock()
	// invalidate stale entries in cache with the fresh discovery data
	for gvCached, cacheEntry := range e.cache.store {
		path := gvRelativeAPIPath(gvCached)
		if discoGV, ok := oapiPathsToGV[path]; !ok || discoGV.ETag() != cacheEntry.etag {
			delete(e.cache.store, gvCached)
		}
	}

	gvPath := gvRelativeAPIPath(gv)
	oapiGV, ok := oapiPathsToGV[gvPath]
	if !ok {
		return nil, fmt.Errorf("cannot find GroupVersion %q in discovery", gvPath)
	}

	// check the cache after invalidating stale data
	parserTuple, ok := e.cache.store[gv]
	// generate new parser on cache miss, etag mismatch
	// defensively cover the case where discovery does not return any ETag
	// for GV, which normally should not happen
	if !ok || parserTuple.etag != oapiGV.ETag() || oapiGV.ETag() == "" {
		freshParser, err := newParserFromOpenAPIGroupVersion(ctx, oapiGV)
		if err != nil {
			return nil, err
		}
		e.cache.store[gv] = &GvkParserCacheEntry{
			parser: freshParser,
			etag:   oapiGV.ETag(),
		}
		return freshParser, nil
	}
	return parserTuple.parser, nil
}

// gvRelativeAPIPath constructs the OpenAPI path for the given GVK
func gvRelativeAPIPath(gv schema.GroupVersion) string {
	if gv.Group == "" {
		return "api/" + gv.Version
	}
	return "apis/" + gv.String()
}

func newParserFromOpenAPIGroupVersion(ctx context.Context, oapiGV OpenAPIGroupVersion) (*GvkParser, error) {
	s, err := oapiGV.Schema(ctx, "application/json")
	if err != nil {
		return nil, err
	}
	var oapi spec3.OpenAPI
	if err := json.Unmarshal(s, &oapi); err != nil {
		return nil, err
	}

	specs := map[string]*spec.Schema{}
	for k, v := range oapi.Components.Schemas {
		specs[k] = v
	}
	return NewGVKParser(specs, false)
}

func (e *cachingUnstructuredExtractor) extractUnstructured(object *unstructured.Unstructured, fieldManager string, subresource string) (*unstructured.Unstructured, error) {
	gvk := object.GroupVersionKind()
	parser, err := e.getParserForGV(e.ctx, gvk.GroupVersion())
	if err != nil {
		return nil, err
	}

	objectType := parser.Type(gvk)
	result := &unstructured.Unstructured{}

	err = managedfields.ExtractInto(object, *objectType, fieldManager, result, subresource) //nolint:forbidigo
	if err != nil {
		return nil, errors.Wrap(err, "failed calling ExtractInto for unstructured")
	}
	result.SetName(object.GetName())
	result.SetNamespace(object.GetNamespace())
	result.SetKind(object.GetKind())
	result.SetAPIVersion(object.GetAPIVersion())
	return result, nil
}

// groupVersionKindExtensionKey is the key used to lookup the
// GroupVersionKind value for an object definition from the
// definition's "extensions" map.
const groupVersionKindExtensionKey = "x-kubernetes-group-version-kind"

// GvkParser contains a Parser that allows introspecting the schema.
type GvkParser struct {
	gvks   map[schema.GroupVersionKind]string
	parser typed.Parser
}

// Type returns a helper which can produce objects of the given type. Any
// errors are deferred until a further function is called.
func (p *GvkParser) Type(gvk schema.GroupVersionKind) *typed.ParseableType {
	typeName, ok := p.gvks[gvk]
	if !ok {
		return nil
	}
	t := p.parser.Type(typeName)
	return &t
}

// NewGVKParser builds a GVKParser from a proto.Models. This
// will automatically find the proper version of the object, and the
// corresponding schema information.
func NewGVKParser(componentNameToSchema map[string]*spec.Schema, preserveUnknownFields bool) (*GvkParser, error) {
	typeSchema, err := schemaconv.ToSchemaFromOpenAPI(componentNameToSchema, preserveUnknownFields)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert models to schema")
	}
	parser := GvkParser{
		gvks: map[schema.GroupVersionKind]string{},
	}
	parser.parser = typed.Parser{Schema: smdschema.Schema{Types: typeSchema.Types}}
	for modelName, ss := range componentNameToSchema {
		gvkList := parseGroupVersionKind(ss.Extensions)
		for _, gvk := range gvkList {
			if len(gvk.Kind) > 0 {
				_, ok := parser.gvks[gvk]
				if ok {
					return nil, fmt.Errorf("duplicate entry for %v", gvk)
				}
				parser.gvks[gvk] = modelName
			}
		}
	}
	return &parser, nil
}

// Get and parse GroupVersionKind from the extension. Returns empty if it doesn't have one.
func parseGroupVersionKind(extensions spec.Extensions) []schema.GroupVersionKind {
	// Get the extensions
	gvkExtension, ok := extensions[groupVersionKindExtensionKey]
	if !ok {
		return []schema.GroupVersionKind{}
	}

	// gvk extension must be a list of at least 1 element.
	gvkList, ok := gvkExtension.([]interface{})
	if !ok {
		return []schema.GroupVersionKind{}
	}

	gvkListResult := make([]schema.GroupVersionKind, 0, len(gvkList))
	for _, gvk := range gvkList {
		// gvk extension list must be a map with group, version, and
		// kind fields
		gvkMap, ok := gvk.(map[string]interface{})
		if !ok {
			continue
		}
		group, ok := gvkMap["group"].(string)
		if !ok {
			continue
		}
		version, ok := gvkMap["version"].(string)
		if !ok {
			continue
		}
		kind, ok := gvkMap["kind"].(string)
		if !ok {
			continue
		}

		gvkListResult = append(gvkListResult, schema.GroupVersionKind{
			Group:   group,
			Version: version,
			Kind:    kind,
		})
	}

	return gvkListResult
}
