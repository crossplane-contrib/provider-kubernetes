package token

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"golang.org/x/oauth2"
)

type ReuseSourceStore struct {
	lock    sync.RWMutex
	sources map[string]oauth2.TokenSource
}

func NewReuseSourceStore() *ReuseSourceStore {
	return &ReuseSourceStore{
		sources: make(map[string]oauth2.TokenSource),
	}
}

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
