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

package client

import (
	"container/list"
	"sync"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// defaultClientCacheSize bounds how many credential-set -> client entries are
// kept per builder. Each entry holds a client with its own RESTMapper and HTTP
// transport; the least recently used entry is evicted beyond the bound.
const defaultClientCacheSize = 64

type cachedClient struct {
	kube client.Client
	rc   *rest.Config
}

// clientCache is an LRU cache of built Kubernetes clients keyed by a digest of
// the credential material that produced them. Building a client is expensive:
// its RESTMapper primes itself via aggregated discovery, a multi-megabyte
// download and decode on CRD-heavy clusters, so clients must be reused across
// reconciles for as long as their credentials remain unchanged.
type clientCache struct {
	mu      sync.Mutex
	size    int
	entries map[string]*list.Element
	order   *list.List // front = most recently used
}

type clientCacheEntry struct {
	key string
	val cachedClient
}

func newClientCache(size int) *clientCache {
	return &clientCache{
		size:    size,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

func (c *clientCache) get(key string) (cachedClient, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return cachedClient{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*clientCacheEntry).val, true
}

func (c *clientCache) put(key string, val cachedClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		el.Value.(*clientCacheEntry).val = val
		return
	}
	c.entries[key] = c.order.PushFront(&clientCacheEntry{key: key, val: val})
	if c.order.Len() > c.size {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*clientCacheEntry).key)
	}
}
