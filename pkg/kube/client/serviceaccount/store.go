package serviceaccount

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/lru"
)

// maxStoreSources is the maximum number of token Sources the Store retains.
// Each entry is keyed by (namespace, name, audiences, expirationSeconds), so
// the cap bounds memory even when ProviderConfigs are frequently created and
// deleted. 256 covers any realistic number of distinct SA identity configs.
const maxStoreSources = 256

// Store reuses token Sources across reconciles, keyed by the TokenRequest
// parameters, so a single cached token is refreshed in place instead of a new
// source (and token) being minted on every reconcile. Evicts least-recently
// used entries beyond maxStoreSources. It is safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	sources *lru.Cache
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{sources: lru.New(maxStoreSources)}
}

// SourceFor returns the Source for the given request, creating and caching one
// on first use.
func (s *Store) SourceFor(client kubernetes.Interface, req Request) *Source {
	key := requestKey(req)

	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.sources.Get(key); ok {
		return v.(*Source)
	}
	src := NewSource(client, req)
	s.sources.Add(key, src)
	return src
}

// requestKey derives a stable key from the TokenRequest parameters.
func requestKey(req Request) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s",
		req.Namespace, req.Name, req.expirationSeconds(), strings.Join(req.Audiences, "\x00"))
	return hex.EncodeToString(h.Sum(nil))
}
