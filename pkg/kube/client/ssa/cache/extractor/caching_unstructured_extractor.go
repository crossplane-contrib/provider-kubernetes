// SPDX-FileCopyrightText: 2024 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/managedfields"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/handler3"
	"k8s.io/kube-openapi/pkg/schemamutation"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// cachingUnstructuredExtractor is an implementation of
// v1.UnstructuredExtractor that caches *GvkParser instances per GV
// using OpenAPI V3 discovery information.
// TODO(erhan): try to upstream this code in kubernetes
type cachingUnstructuredExtractor struct {
	// added as field to not break the interface for other funcs, instantiated at each reconcile
	ctx   context.Context
	cache *GVKParserCache
	dc    discovery.DiscoveryInterface
}

// NewCachingUnstructuredExtractor returns a new cachingUnstructuredExtractor
func NewCachingUnstructuredExtractor(ctx context.Context, dc discovery.DiscoveryInterface, cache *GVKParserCache) (applymetav1.UnstructuredExtractor, error) {
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

func discoveryPaths(ctx context.Context, rc rest.Interface) (map[string]OpenAPIGroupVersion, map[string]string, error) {
	data, err := rc.Get().AbsPath("/openapi/v3").Do(ctx).Raw()
	if err != nil {
		return nil, nil, err
	}

	discoMap := &handler3.OpenAPIV3Discovery{}
	err = json.Unmarshal(data, discoMap)
	if err != nil {
		return nil, nil, err
	}

	oapiPathsToGV := map[string]OpenAPIGroupVersion{}
	oapiPathsToETags := map[string]string{}
	for path, oapiGV := range discoMap.Paths {
		parse, err := url.Parse(oapiGV.ServerRelativeURL)
		if err != nil {
			return nil, nil, err
		}
		useClientPrefix := strings.HasPrefix(oapiGV.ServerRelativeURL, "/openapi/v3")
		etag := parse.Query().Get("hash")
		oapiPathsToGV[path] = newCustomOAPIGroupVersion(rc, oapiGV, useClientPrefix)
		oapiPathsToETags[path] = etag

	}
	return oapiPathsToGV, oapiPathsToETags, nil
}

// getParserForGV fetches the *GVKParser for the given GroupVersion.
// Results are cached by utilizing the hashes returned by the OpenAPI v3
// discovery endpoint
func (e *cachingUnstructuredExtractor) getParserForGV(ctx context.Context, gv schema.GroupVersion) (*GvkParser, error) { //nolint:gocyclo // for atomic cache operations
	// obtain GroupVersion API paths and hashes of associated schemas
	// from discovery endpoint
	oapiPathsToGV, oapiPathsToETags, err := discoveryPaths(ctx, e.dc.RESTClient())
	if err != nil {
		return nil, err
	}

	e.cache.mu.Lock()
	// invalidate stale entries in cache with the fresh discovery data
	for gvCached, cacheEntry := range e.cache.store {
		path := gvRelativeAPIPath(gvCached)
		schemaEtag, discovered := oapiPathsToETags[path]
		if !discovered || schemaEtag != cacheEntry.etag {
			delete(e.cache.store, gvCached)
		}
	}
	e.cache.mu.Unlock()

	gvPath := gvRelativeAPIPath(gv)
	oapiGV, ok := oapiPathsToGV[gvPath]
	if !ok {
		return nil, fmt.Errorf("cannot find GroupVersion %q in discovery", gvPath)
	}
	schemaETag, ok := oapiPathsToETags[gvPath]
	if !ok {
		return nil, fmt.Errorf("cannot find ETag for GroupVersion %q in discovery", gvPath)
	}

	// check the cache after invalidating stale data
	e.cache.mu.RLock()
	parserTuple, ok := e.cache.store[gv]
	e.cache.mu.RUnlock()
	if ok && parserTuple.etag == schemaETag && schemaETag != "" {
		// cache hit
		return parserTuple.parser, nil
	}
	// generate new parser on cache miss or etag mismatch
	// defensively cover the case where discovery does not return any ETag
	// for GV, which normally should not happen
	//
	// concurrent schema fetches and cache updates for the same GV
	// (of the particular k8s cluster) are deduplicated with singleflight.
	freshParserObj, err, _ := e.cache.sf.Do(gv.String(), func() (any, error) {
		freshParser, err := newParserFromOpenAPIGroupVersion(ctx, oapiGV)
		if err != nil {
			return nil, err
		}
		if schemaETag != "" {
			e.cache.mu.Lock()
			defer e.cache.mu.Unlock()
			e.cache.store[gv] = &gvkParserCacheEntry{
				parser: freshParser,
				etag:   schemaETag,
			}
		}
		return freshParser, nil
	})
	if err != nil {
		return nil, err
	}
	freshParser, ok := freshParserObj.(*GvkParser)
	if !ok {
		return nil, fmt.Errorf("type assertion error: expected GvkParser, got %T", freshParserObj)
	}
	return freshParser, nil
}

// gvRelativeAPIPath constructs the OpenAPI path for the given GVK
func gvRelativeAPIPath(gv schema.GroupVersion) string {
	if gv.Group == "" {
		return "api/" + gv.Version
	}
	return "apis/" + gv.String()
}

func newParserFromOpenAPIGroupVersion(ctx context.Context, oapiGV OpenAPIGroupVersion) (*GvkParser, error) {
	// note: although proto schema is more performant, we are
	// using the JSON schema here, as there is an issue with
	// proto.NewOpenAPIV3Data during makeUnions() at
	// https://github.com/kubernetes/kube-openapi/blob/f7e401e7b4c2199f15e2cf9e37a2faa2209f286a/pkg/schemaconv/smd.go#L128
	s, err := oapiGV.Schema(ctx, "application/json")
	if err != nil {
		return nil, errors.Wrap(err, "cannot get OpenAPI schema")
	}
	var oapi spec3.OpenAPI
	if err := json.Unmarshal(s, &oapi); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal OpenAPI schema")
	}

	var refErrors []error
	// validate that every reference in each schema in the OpenAPI document
	// is in the document, i.e. OpenAPI document is self-contained
	// with no unresolvable or external reference.
	// errors are expected to be accumulated into refErrors,
	// by the RefCallback function,
	// as the schema walker has no means of stopping early
	walker := schemamutation.Walker{
		SchemaCallback: schemamutation.SchemaCallBackNoop,
		// note: this should not mutate any ref, only validate
		RefCallback: validateRefSelfContainedFn(&refErrors, oapi.Components.Schemas),
	}
	specs := map[string]*spec.Schema{}
	for k, v := range oapi.Components.Schemas {
		walker.WalkSchema(v)
		specs[k] = v
	}
	if aggRefErrors := kerrors.NewAggregate(refErrors); aggRefErrors != nil {
		return nil, errors.Wrap(aggRefErrors, "cannot validate references in OpenAPI schemas")
	}
	// use the forked version of the new GVK parser
	// accepting a map of components to OpenAPI schemas
	// instead of proto.Models
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

// validateRefSelfContainedFn returns a RefCallback function for
// schemamutation.Walker that defensively checks whether the ref is
// contained in the given schema collection,
// i.e. the ref does not point any remote/outside location.
//
// for each non-conformant ref, errors are accumulated to the provided string slice
// as this function is intended to be used with the schemamutation.Walker
func validateRefSelfContainedFn(errs *[]error, oapiComponentsToSchema map[string]*spec.Schema) func(ref *spec.Ref) *spec.Ref { //nolint:gocyclo
	return func(ref *spec.Ref) *spec.Ref {
		switch {
		case ref == nil, ref.String() == "":
			// do nothing
		case ref.RemoteURI() != "":
			*errs = append(*errs, fmt.Errorf("only local references are supported, got remote URI: %s", ref.String()))
		case ref.IsCanonical():
			*errs = append(*errs, fmt.Errorf("only local references are supported, got canonical path: %s", ref.String()))
		case ref.GetPointer() != nil && ref.GetURL() != nil && ref.HasFragmentOnly:
			// we only expect local references in the form of URL fragment "#/component/schemas/{componentName}"
			tokens := ref.GetPointer().DecodedTokens()
			if len(tokens) != 3 || tokens[0] != "components" || tokens[1] != "schemas" {
				*errs = append(*errs, fmt.Errorf("expected local ref with #/components/schemas/{componentName}, got: %s", ref.String()))
				break
			}
			if _, ok := oapiComponentsToSchema[tokens[2]]; !ok {
				*errs = append(*errs, fmt.Errorf("local reference %s cannot be found in OpenAPI schemas", ref.String()))
				break
			}
			// passed validation
		default:
			*errs = append(*errs, fmt.Errorf("only local references are supported, got: %s", ref.String()))
		}
		return ref
	}
}
