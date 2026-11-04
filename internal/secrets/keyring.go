package secrets

import (
	"encoding/base64"
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

var errRingNotFound = keyring.ErrNotFound

type ring interface {
	Get(service, user string) (string, error)
	Set(service, user, value string) error
	Delete(service, user string) error
}
type systemRing struct{}

func (systemRing) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (systemRing) Set(s, u, v string) error        { return keyring.Set(s, u, v) }
func (systemRing) Delete(s, u string) error        { return keyring.Delete(s, u) }

type keyringStore struct {
	mu        sync.Mutex
	ring      ring
	namespace string
}

func (s *keyringStore) service() string  { return "telegram-mcp/" + s.namespace }
func (*keyringStore) Backend() Backend   { return Keyring }
func (*keyringStore) Fallback() bool     { return false }
func (*keyringStore) String() string     { return "secrets.Store(keyring; redacted)" }
func (s *keyringStore) GoString() string { return s.String() }
func (s *keyringStore) Get(k Key) ([]byte, error) {
	if !validKey(k) {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.ring.Get(s.service(), string(k))
	if errors.Is(err, errRingNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, ErrCorrupt
	}
	return b, nil
}
func (s *keyringStore) Set(k Key, v []byte) error {
	if !validKey(k) {
		return ErrInvalid
	}
	// Stay below both the Windows blob limit after base64 and macOS security's
	// command limit after the provider's second encoding. The latter starts a
	// subprocess before checking size; reject here to avoid leaking that process.
	if len(v) > 1800 {
		return ErrTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ring.Set(s.service(), string(k), base64.StdEncoding.EncodeToString(v)); err != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *keyringStore) Delete(k Key) error {
	if !validKey(k) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove(k)
}
func (s *keyringStore) remove(k Key) error {
	if err := s.ring.Delete(s.service(), string(k)); err != nil && !errors.Is(err, errRingNotFound) {
		return ErrUnavailable
	}
	return nil
}
func (s *keyringStore) DeleteAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result error
	// Always attempt every known key, even if one backend operation fails.
	for _, k := range allKeys {
		if err := s.remove(k); err != nil {
			result = err
		}
	}
	return result
}
