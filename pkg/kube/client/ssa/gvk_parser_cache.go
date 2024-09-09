// SPDX-FileCopyrightText: 2024 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package ssa

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

// GVKParserCacheManager maintains GVK parser cache stores for each provider config.
type GVKParserCacheManager struct {
	// cacheStore holds the *GvkParserCache per provider configuration.
	// The cacheStore key is the UID of the provider config object.
	cacheStore map[types.UID]*GvkParserCache
	// mu is used to make sure the cacheStore map is concurrency-safe.
	mu *sync.RWMutex
}

// GVKParserCacheManagerOption lets you configure a *GVKParserCacheManager.
type GVKParserCacheManagerOption func(cache *GVKParserCacheManager)

// NewGVKParserCacheManager returns a new empty *GVKParserCacheManager.
func NewGVKParserCacheManager(opts ...GVKParserCacheManagerOption) *GVKParserCacheManager {
	c := &GVKParserCacheManager{
		cacheStore: map[types.UID]*GvkParserCache{},
		mu:         &sync.RWMutex{},
	}
	for _, f := range opts {
		f(c)
	}
	return c
}

// LoadOrNewCacheForProviderConfig returns the *GvkParserCache for the given provider config,
// initializing an empty cache for the first use.
// the implementation is concurrency-safe.
func (cm *GVKParserCacheManager) LoadOrNewCacheForProviderConfig(pc *v1alpha1.ProviderConfig) (*GvkParserCache, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	sc, ok := cm.cacheStore[pc.GetUID()]
	if !ok {
		sc = &GvkParserCache{
			store: map[schema.GroupVersion]*GvkParserCacheEntry{},
			mu:    &sync.RWMutex{},
		}
		cm.cacheStore[pc.GetUID()] = sc
	}
	return sc, nil
}

// RemoveCache removes the cache for the given provider config.
func (cm *GVKParserCacheManager) RemoveCache(pc *v1alpha1.ProviderConfig) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.cacheStore, pc.GetUID())
}

// GvkParserCache holds the cached parser instances and the ETags
// of the associated provider config.
// Parsers are generated and cached per GroupVersion
type GvkParserCache struct {
	// Parsers per GroupVersion
	store map[schema.GroupVersion]*GvkParserCacheEntry
	mu    *sync.RWMutex
}

// GvkParserCacheEntry wraps the *GvkParser with an ETag for
// freshness check against discovery data
type GvkParserCacheEntry struct {
	parser *GvkParser
	etag   string
}
