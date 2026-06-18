package nebius

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// SDKStore caches Nebius IAM token sources keyed by the credentials they were
// built from, so a single background-refreshing gosdk client is reused across
// reconciles instead of leaking one (with its goroutines and gRPC connection)
// per reconcile.
type SDKStore struct {
	lock    sync.RWMutex
	sources map[string]tokenSource
}

// NewSDKStore creates a new SDKStore.
func NewSDKStore() *SDKStore {
	return &SDKStore{sources: make(map[string]tokenSource)}
}

// SourceForCredentials returns a cached token source for the supplied
// credentials, building and caching one via build if none exists yet. The
// source is built while holding the write lock so concurrent reconciles for
// the same credentials never construct (and leak) more than one client.
func (s *SDKStore) SourceForCredentials(ctx context.Context, credentials []byte, build func(context.Context) (tokenSource, error)) (tokenSource, error) {
	key := hashCredentials(credentials)

	s.lock.RLock()
	src, ok := s.sources[key]
	s.lock.RUnlock()
	if ok {
		return src, nil
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	if src, ok := s.sources[key]; ok {
		return src, nil
	}
	src, err := build(ctx)
	if err != nil {
		return nil, err
	}
	s.sources[key] = src
	return src, nil
}

func hashCredentials(credentials []byte) string {
	h := sha256.New()
	h.Write(credentials)
	return hex.EncodeToString(h.Sum(nil))
}
