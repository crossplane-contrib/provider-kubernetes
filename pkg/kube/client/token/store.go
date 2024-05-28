package token

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"golang.org/x/oauth2"
)

// ReuseSourceStore is a store for reuse token sources to avoid creating new
// sources for the same refresh token in each reconciliation loop.
type ReuseSourceStore struct {
	lock    sync.RWMutex
	sources map[string]oauth2.TokenSource
}

// NewReuseSourceStore creates a new ReuseSourceStore.
func NewReuseSourceStore() *ReuseSourceStore {
	return &ReuseSourceStore{
		sources: make(map[string]oauth2.TokenSource),
	}
}

// SourceForRefreshToken returns a token source for the supplied refresh token.
// If a token source for the refresh token already exists, it is returned.
// Otherwise, a new reuse token source is created, stored for later access and
// returned.
func (c *ReuseSourceStore) SourceForRefreshToken(refreshToken string, src oauth2.TokenSource) oauth2.TokenSource {
	key := hashToken(refreshToken)

	c.lock.RLock()
	source, exists := c.sources[key]
	c.lock.RUnlock()

	if exists {
		return source
	}

	source = oauth2.ReuseTokenSource(nil, src)

	c.lock.Lock()
	c.sources[key] = source
	c.lock.Unlock()

	return source
}

func hashToken(token string) string {
	h := sha256.New()
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}
