/*
Copyright 2018 The Kubernetes Authors.

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
This file is forked from upstream kubernetes/apimachinery
https://github.com/kubernetes/apimachinery/blob/2465dc5239ab8827a637148a78b380c278b4a5f4/pkg/util/managedfields/gvkparser.go

The reason is to be able to construct GvkParser objects
from a map of components to spec.Schema (obtained from OpenAPI JSON document),
instead of proto.Models (obtained from OpenAPI Protobuf document), as
building a protobuf data has an upstream issue at
proto.NewOpenAPIV3Data during makeUnions()
https://github.com/kubernetes/kube-openapi/blob/f7e401e7b4c2199f15e2cf9e37a2faa2209f286a/pkg/schemaconv/smd.go#L128
*/

package extractor

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/schemaconv"
	"k8s.io/kube-openapi/pkg/validation/spec"
	smdschema "sigs.k8s.io/structured-merge-diff/v6/schema"
	"sigs.k8s.io/structured-merge-diff/v6/typed"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// groupVersionKindExtensionKey is the key used to lookup the
// GroupVersionKind value for an object definition from the
// definition's "extensions" map.
const groupVersionKindExtensionKey = "x-kubernetes-group-version-kind"

// GvkParser contains a Parser that allows introspecting the schema.
type GvkParser struct {
	gvks map[schema.GroupVersionKind]string
	// ambiguous holds the sorted candidate schema names of each
	// GroupVersionKind that more than one schema declares and that could not
	// be resolved to a single schema.
	ambiguous map[schema.GroupVersionKind][]string
	parser    typed.Parser
}

// Type returns a helper which can produce objects of the given type. Any
// errors are deferred until a further function is called. It returns nil if
// no schema, or no single schema, declares the GroupVersionKind.
func (p *GvkParser) Type(gvk schema.GroupVersionKind) *typed.ParseableType {
	typeName, ok := p.gvks[gvk]
	if !ok {
		return nil
	}
	t := p.parser.Type(typeName)
	return &t
}

// TypeOrError is like Type, but returns an error that explains why there is
// no type for the GroupVersionKind.
func (p *GvkParser) TypeOrError(gvk schema.GroupVersionKind) (*typed.ParseableType, error) {
	if candidates, ok := p.ambiguous[gvk]; ok {
		return nil, fmt.Errorf("ambiguous schemas for %v: %s", gvk, strings.Join(candidates, ", "))
	}
	t := p.Type(gvk)
	if t == nil {
		return nil, fmt.Errorf("no schema for %v", gvk)
	}
	return t, nil
}

// NewGVKParser builds a GVKParser from a directory of OpenAPI schema
// (instead of proto.Models in upstream k8s apimachinery implementation)
//
// This will automatically find the proper version of the object, and the
// corresponding schema information.
//
// A GroupVersionKind that more than one schema declares is ambiguous: Type
// returns nil and TypeOrError returns an error for it. Other
// GroupVersionKinds are unaffected.
func NewGVKParser(componentNameToSchema map[string]*spec.Schema, preserveUnknownFields bool) (*GvkParser, error) {
	return newGVKParser(componentNameToSchema, preserveUnknownFields, nil)
}

// newGVKParser is NewGVKParser, but resolves a GroupVersionKind that several
// schemas declare to the single one among them in pathReferenced, the schemas
// the document's paths reference. Otherwise the GroupVersionKind is ambiguous.
// OpenShift, for example, declares SecurityContextConstraints in both
// io.openshift.security.v1 (referenced) and
// com.github.openshift.api.security.v1.
//
// note: the upstream managedfields.NewGVKParserFromOpenAPIV3
// (kubernetes/kubernetes#142048) keeps an arbitrary schema for a duplicated
// GroupVersionKind, which breaks SecurityContextConstraints. Keep this
// resolution when switching to it.
func newGVKParser(componentNameToSchema map[string]*spec.Schema, preserveUnknownFields bool, pathReferenced map[string]bool) (*GvkParser, error) {
	typeSchema, err := schemaconv.ToSchemaFromOpenAPI(componentNameToSchema, preserveUnknownFields)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert models to schema")
	}
	parser := GvkParser{
		gvks:      map[schema.GroupVersionKind]string{},
		ambiguous: map[schema.GroupVersionKind][]string{},
	}
	parser.parser = typed.Parser{Schema: smdschema.Schema{Types: typeSchema.Types}}

	candidates := map[schema.GroupVersionKind][]string{}
	for modelName, ss := range componentNameToSchema {
		for _, gvk := range parseGroupVersionKind(ss.Extensions) {
			if len(gvk.Kind) > 0 {
				candidates[gvk] = append(candidates[gvk], modelName)
			}
		}
	}
	for gvk, names := range candidates {
		if len(names) == 1 {
			parser.gvks[gvk] = names[0]
			continue
		}
		var referenced []string
		for _, name := range names {
			if pathReferenced[name] {
				referenced = append(referenced, name)
			}
		}
		if len(referenced) == 1 {
			parser.gvks[gvk] = referenced[0]
			continue
		}
		sort.Strings(names)
		parser.ambiguous[gvk] = names
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
