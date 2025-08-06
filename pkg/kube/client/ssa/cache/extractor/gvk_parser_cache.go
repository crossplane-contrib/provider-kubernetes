// SPDX-FileCopyrightText: 2024 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"sync"

	xpresource "github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"golang.org/x/sync/singleflight"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// GVKParserCacheManager maintains GVK parser cache stores for each provider config.
// The implementation is thread-safe.
type GVKParserCacheManager struct {
	// mu is used to make sure the cacheStore map is concurrency-safe.
	mu sync.Mutex
	// cacheStore holds the *GVKParserCache per provider configuration.
	// The cacheStore key is the UID of the provider config object.
	cacheStore map[types.UID]*GVKParserCache
}

// GVKParserCacheManagerOption lets you configure a *GVKParserCacheManager.
type GVKParserCacheManagerOption func(cache *GVKParserCacheManager)

// NewGVKParserCacheManager returns a new empty *GVKParserCacheManager.
func NewGVKParserCacheManager(opts ...GVKParserCacheManagerOption) *GVKParserCacheManager {
	c := &GVKParserCacheManager{
		cacheStore: map[types.UID]*GVKParserCache{},
	}
	for _, f := range opts {
		f(c)
	}
	return c
}

// LoadOrNewCacheForProviderConfig returns the *GVKParserCache for the given provider config,
// initializing an empty cache for the first use.
// the implementation is concurrency-safe.
func (cm *GVKParserCacheManager) LoadOrNewCacheForProviderConfig(pc xpresource.ProviderConfig) (*GVKParserCache, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	sc, ok := cm.cacheStore[pc.GetUID()]
	if !ok {
		sc = &GVKParserCache{
			store: map[schema.GroupVersion]*gvkParserCacheEntry{},
		}
		cm.cacheStore[pc.GetUID()] = sc
	}
	return sc, nil
}

// RemoveCache removes the cache for the given provider config.
func (cm *GVKParserCacheManager) RemoveCache(pc xpresource.ProviderConfig) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.cacheStore, pc.GetUID())
}

// GVKParserCache holds the cached parser instances and the ETags
// of the associated provider config.
// Parsers are generated and cached per GroupVersion
type GVKParserCache struct {
	mu    sync.RWMutex
	store map[schema.GroupVersion]*gvkParserCacheEntry

	// sf is for ensuring a single in-flight OpenAPI
	// schema requests for a GV
	sf singleflight.Group
}

// gvkParserCacheEntry wraps the *GvkParser with an ETag for
// freshness check against discovery data
type gvkParserCacheEntry struct {
	parser *GvkParser
	etag   string
}
