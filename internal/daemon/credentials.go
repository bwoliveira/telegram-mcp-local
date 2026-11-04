package daemon

import (
	"errors"
	"os"
	"strconv"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine"
	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

// ErrCredentialsRequired signals first use without a complete API credential pair.
var ErrCredentialsRequired = errors.New("Telegram API credentials are required")

// SaveAccountCredentials seeds the same pinned vault used by headless startup.
// It never changes environment variables or replaces existing credentials.
func SaveAccountCredentials(dataDir string, account engine.AccountID, id, hash string) error {
	if _, err := validateCredentials(id, hash); err != nil {
		return err
	}
	return service.WithAccountSecrets(dataDir, string(account), func(s secrets.Store) error {
		_, _, _, err := prepareCredentialValues(s, id, hash)
		return err
	})
}

func validateCredentials(rawID, hash string) (int, error) {
	id, err := strconv.Atoi(rawID)
	if err != nil || id <= 0 || len(hash) != 32 {
		return 0, secrets.ErrInvalid
	}
	for _, c := range rawID {
		if c < '0' || c > '9' {
			return 0, secrets.ErrInvalid
		}
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return 0, secrets.ErrInvalid
		}
	}
	return id, nil
}

// PrepareAccountCredentials seeds API credentials on first use only. Later
// processes use the same pinned secrets backend without secrets in unit files.
// No session load or network access occurs here.
func PrepareAccountCredentials(dataDir string, account engine.AccountID) (int, engine.Secret, engine.SessionStore, error) {
	var id int
	var hash engine.Secret
	var session engine.SessionStore
	err := service.WithAccountSecrets(dataDir, string(account), func(s secrets.Store) error {
		var err error
		id, hash, session, err = prepareCredentials(s)
		return err
	})
	return id, hash, session, err
}

func prepareCredentials(s secrets.Store) (int, engine.Secret, engine.SessionStore, error) {
	return prepareCredentialValues(s, os.Getenv("TELEGRAM_API_ID"), os.Getenv("TELEGRAM_API_HASH"))
}

func prepareCredentialValues(s secrets.Store, envID, envHash string) (int, engine.Secret, engine.SessionStore, error) {
	rawID, idErr := s.Get(secrets.APIID)
	rawHash, hashErr := s.Get(secrets.APIHash)
	for _, e := range []error{idErr, hashErr} {
		if e != nil && !errors.Is(e, secrets.ErrNotFound) {
			return 0, "", nil, e
		}
	}
	if errors.Is(idErr, secrets.ErrNotFound) {
		rawID = []byte(envID)
	} else if envID != "" && envID != string(rawID) {
		return 0, "", nil, errors.New("TELEGRAM_API_ID conflicts with stored credential")
	}
	if errors.Is(hashErr, secrets.ErrNotFound) {
		rawHash = []byte(envHash)
	} else if envHash != "" && envHash != string(rawHash) {
		return 0, "", nil, errors.New("TELEGRAM_API_HASH conflicts with stored credential")
	}
	if len(rawID) == 0 || len(rawHash) == 0 {
		return 0, "", nil, ErrCredentialsRequired
	}
	id, err := validateCredentials(string(rawID), string(rawHash))
	if err != nil {
		return 0, "", nil, err
	}
	if errors.Is(idErr, secrets.ErrNotFound) {
		if err := s.Set(secrets.APIID, rawID); err != nil {
			return 0, "", nil, err
		}
	}
	if errors.Is(hashErr, secrets.ErrNotFound) {
		if err := s.Set(secrets.APIHash, rawHash); err != nil {
			return 0, "", nil, err
		}
	}
	return id, engine.Secret(rawHash), gotdengine.SecretsSession{Store: s}, nil
}
