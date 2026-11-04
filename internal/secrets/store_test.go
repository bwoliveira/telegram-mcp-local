package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

type fakeRing struct {
	values  map[string]string
	failure error
}

func (f *fakeRing) Get(service, user string) (string, error) {
	if f.failure != nil {
		return "", f.failure
	}
	v, ok := f.values[service+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (f *fakeRing) Set(service, user, value string) error {
	if f.failure != nil {
		return f.failure
	}
	f.values[service+user] = value
	return nil
}
func (f *fakeRing) Delete(service, user string) error {
	if f.failure != nil {
		return f.failure
	}
	delete(f.values, service+user)
	return nil
}

func TestKeyringRoundTripAndAccountIsolation(t *testing.T) {
	ring := &fakeRing{values: map[string]string{}}
	a, err := openWith("account-a", Options{Mode: "keyring"}, ring, "linux")
	if err != nil {
		t.Fatal(err)
	}
	b, err := openWith("account-b", Options{Mode: "keyring"}, ring, "linux")
	if err != nil {
		t.Fatal(err)
	}
	value := []byte{0, 1, 2, 255}
	for _, k := range allKeys {
		if err := a.Set(k, value); err != nil {
			t.Fatal(err)
		}
		got, err := a.Get(k)
		if err != nil || !bytes.Equal(got, value) {
			t.Fatalf("round trip failed: %v", err)
		}
		if _, err := b.Get(k); !errors.Is(err, ErrNotFound) {
			t.Fatalf("account isolation failed: %v", err)
		}
	}
	if err := a.Delete(APIID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Get(APIID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := a.DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if len(ring.values) != 0 {
		t.Fatal("account secrets remain")
	}
	if err := a.DeleteAll(); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringSizeAndCorruption(t *testing.T) {
	ring := &fakeRing{values: map[string]string{}}
	store, err := openWith("a", Options{Mode: "keyring"}, ring, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(Session, bytes.Repeat([]byte{1}, 1800)); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(Session, bytes.Repeat([]byte{1}, 1801)); !errors.Is(err, ErrTooLarge) {
		t.Fatal("oversized value accepted")
	}
	s := store.(*keyringStore)
	ring.values[s.service()+string(Session)] = "malformed base64!"
	if _, err := store.Get(Session); !errors.Is(err, ErrCorrupt) {
		t.Fatal("malformed keyring value accepted")
	}
}

func TestErrorsAndFormattingDoNotExposeSecrets(t *testing.T) {
	const secret = "synthetic-sensitive-test-value"
	ring := &fakeRing{values: map[string]string{}}
	s, err := openWith(secret, Options{Mode: "keyring"}, ring, "linux")
	if err != nil {
		t.Fatal(err)
	}
	ring.failure = errors.New(secret)
	errs := []error{s.Set(Session, []byte(secret)), s.Delete(Session), s.DeleteAll(), s.Set(Key(secret), []byte(secret))}
	_, err = s.Get(Session)
	errs = append(errs, err)
	_, err = openWith(secret, Options{Mode: "keyring"}, ring, "linux")
	errs = append(errs, err)
	for _, err := range errs {
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe or absent error")
		}
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, s), secret) {
			t.Fatal("unsafe store formatting")
		}
	}
}
