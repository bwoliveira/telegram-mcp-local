package panel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCredentialSetupFormAndBoundary(t *testing.T) {
	calls := 0
	const marker = "synthetic-secret-marker"
	handler := CredentialSetup(func(id, hash string) error {
		calls++
		if id != "123" || hash != marker {
			t.Fatal("form values not delivered")
		}
		return errors.New(marker)
	})
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://localhost/", strings.NewReader("api_id=123&api_hash="+marker))
		r.Header.Set("Origin", "http://localhost")
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal("network bypassed desktop boundary")
		}
	}
	if calls != 0 {
		t.Fatal("untrusted call reached vault")
	}
	h := Internal(handler)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	for _, want := range []string{`name="api_id"`, `name="api_hash"`, `type="password"`, `https://my.telegram.org/apps`, `pattern="[0-9]+"`, `pattern="[a-fA-F0-9]{32}"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(url.Values{"api_id": {"123"}, "api_hash": {marker}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(w, r)
	if calls != 1 || w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), marker) {
		t.Fatal("failure leaked secrets or lost retry form")
	}
	if !strings.Contains(w.Body.String(), `name="api_hash"`) {
		t.Fatal("cannot retry")
	}
}

func TestCredentialSetupSuccessAndRequestLimit(t *testing.T) {
	calls := 0
	h := Internal(CredentialSetup(func(id, hash string) error { calls++; return nil }))
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(strings.Repeat("x", 9000)); w.Code != http.StatusBadRequest || calls != 0 {
		t.Fatal("oversized request accepted")
	}
	if w := post("api_id=123&api_hash=synthetic"); w.Code != 200 || !strings.Contains(w.Body.String(), `url=/`) || calls != 1 {
		t.Fatal("success did not navigate to QR flow")
	}
	post("api_id=124&api_hash=synthetic")
	if calls != 1 {
		t.Fatal("stale form resubmitted saved credentials")
	}
}
