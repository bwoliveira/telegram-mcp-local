package secrets

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

type readFailureRing struct {
	*fakeRing
	err error
}

func (r readFailureRing) Get(service, user string) (string, error) { return "", r.err }

func TestWindowsKeyringAbsenceDoesNotHideVaultFailure(t *testing.T) {
	for _, tt := range []struct {
		name          string
		backend, want error
	}{
		{"missing item", keyring.ErrNotFound, ErrNotFound},
		{"inaccessible vault", errors.New("Credential Manager access denied"), ErrUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ring := readFailureRing{fakeRing: &fakeRing{values: map[string]string{}}, err: tt.backend}
			s, err := openWith("windows-first-run-test", Options{Mode: "keyring"}, ring, "windows")
			if err == nil {
				_, err = s.Get(APIID)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Get: %v, want %v", err, tt.want)
			}
			if tt.want != ErrNotFound && errors.Is(err, ErrNotFound) {
				t.Fatal("vault failure was treated as an absent credential")
			}
		})
	}
}
