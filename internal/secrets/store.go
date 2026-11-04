package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
)

// Key is one of the three account credentials. Arbitrary keys are rejected.
type Key string

const (
	APIID   Key = "api_id"
	APIHash Key = "api_hash"
	Session Key = "session"
)

var allKeys = [...]Key{APIID, APIHash, Session}

func validKey(k Key) bool { return k == APIID || k == APIHash || k == Session }

// Errors deliberately exclude input values, filesystem paths and provider text.
var (
	ErrNotFound    = errors.New("secrets: credential not found")
	ErrInvalid     = errors.New("secrets: invalid configuration or key")
	ErrUnavailable = errors.New("secrets: keyring unavailable")
	ErrPermission  = errors.New("secrets: unsafe filesystem permissions or type")
	ErrStorage     = errors.New("secrets: storage operation failed")
	ErrCorrupt     = errors.New("secrets: authentication or data validation failed")
	ErrTooLarge    = errors.New("secrets: credential exceeds backend size limit")
)

type Backend string

const (
	Keyring Backend = "keyring"
	File    Backend = "file"
)

// Store is bound to a single account. Implementations are safe for concurrent use.
// Delete and DeleteAll are idempotent. No method logs any secret or identifier.
type Store interface {
	Get(Key) ([]byte, error)
	Set(Key, []byte) error
	Delete(Key) error
	DeleteAll() error
	Backend() Backend
	Fallback() bool
}

// Options overrides selection and Linux-only encrypted-file locations. Empty Mode
// reads TELEGRAM_MCP_SECRETS, then auto-selects. Paths default to the XDG locations.
type Options struct {
	Mode    string
	DataDir string
	KeyDir  string
}

// Open selects and validates an account store, without creating credentials.
func Open(account string, options Options) (Store, error) {
	return openWith(account, options, systemRing{}, runtime.GOOS)
}
func openWith(account string, o Options, r ring, goos string) (Store, error) {
	if account == "" {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256([]byte(account))
	namespace := hex.EncodeToString(sum[:])
	mode := o.Mode
	if mode == "" {
		mode = os.Getenv("TELEGRAM_MCP_SECRETS")
	}
	if mode != "" && mode != "keyring" && mode != "file" {
		return nil, ErrInvalid
	}
	fallback := false
	if mode != "file" {
		s := &keyringStore{ring: r, namespace: namespace}
		if r != nil {
			_, err := r.Get(s.service(), string(APIID))
			if err == nil || errors.Is(err, errRingNotFound) {
				return s, nil
			}
		}
		if mode == "keyring" || goos != "linux" {
			return nil, ErrUnavailable
		}
		fallback = true
	}
	if goos != "linux" {
		return nil, ErrInvalid
	}
	return newFileStore(namespace, o, fallback)
}
