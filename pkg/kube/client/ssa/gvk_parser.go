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
*/

package ssa

import (
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/schemaconv"
	"k8s.io/kube-openapi/pkg/validation/spec"
	smdschema "sigs.k8s.io/structured-merge-diff/v4/schema"
	"sigs.k8s.io/structured-merge-diff/v4/typed"
)

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

// NewGVKParser builds a GVKParser from a directory of OpenAPI schema
// (instead of proto.Models in upstream k8s apimachinery implementation)
//
// This will automatically find the proper version of the object, and the
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
