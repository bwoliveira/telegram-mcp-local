package updater

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	testSeed  = [ed25519.SeedSize]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	testKeyID = [8]byte{0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78}
)

func testPublicKey() string {
	pub := ed25519.NewKeyFromSeed(testSeed[:]).Public().(ed25519.PublicKey)
	raw := append([]byte("Ed"), testKeyID[:]...)
	raw = append(raw, pub...)
	return base64.StdEncoding.EncodeToString(raw)
}

func signTestManifest(message []byte) []byte {
	priv := ed25519.NewKeyFromSeed(testSeed[:])
	sig := ed25519.Sign(priv, message)
	raw := append([]byte("Ed"), testKeyID[:]...)
	raw = append(raw, sig...)
	trusted := "timestamp:0\tfile:checksums.txt\tprehashed"
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), trusted...))
	return []byte("untrusted comment: signature from test key\n" +
		base64.StdEncoding.EncodeToString(raw) + "\ntrusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n")
}

func testManifest(version, name string, body []byte) []byte {
	return []byte(fmt.Sprintf("# version: %s\n# source_commit: %s\n# date: 2026-11-03T12:00:00Z\n%x  %s\n",
		version, strings.Repeat("a", 40), sha256.Sum256(body), name))
}

func otherTestPublicKey() string {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 101)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	raw := append([]byte("Ed"), []byte{1, 3, 5, 7, 9, 11, 13, 15}...)
	raw = append(raw, pub...)
	return base64.StdEncoding.EncodeToString(raw)
}

func TestVerifyRealMinisignVector(t *testing.T) {
	message, err := os.ReadFile("testdata/minisign/checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile("testdata/minisign/checksums.txt.minisig")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := os.ReadFile("testdata/minisign/public.key")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMinisign(message, signature, string(publicKey)); err != nil {
		t.Fatalf("real minisign -S -l vector rejected: %v", err)
	}
}

func TestVerifyMinisignLegacyEd(t *testing.T) {
	message := testManifest("2.0.0", "file", []byte("payload"))
	if err := verifyMinisign(message, signTestManifest(message), testPublicKey()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		msg  []byte
		sig  []byte
		key  string
	}{
		{"tampered manifest", append(append([]byte(nil), message...), 'x'), signTestManifest(message), testPublicKey()},
		{"invalid signature", message, []byte("invalid\n"), testPublicKey()},
		{"wrong key", message, signTestManifest(message), otherTestPublicKey()},
		{"missing signature", message, nil, testPublicKey()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verifyMinisign(tc.msg, tc.sig, tc.key); err == nil {
				t.Fatal("verification unexpectedly succeeded")
			}
		})
	}
}

func TestParseReleaseManifestRejectsRollbackAndMetadataMismatch(t *testing.T) {
	body := []byte("payload")
	name := "Telegram-MCP-x86_64.AppImage"
	for _, tc := range []struct {
		name, current, requested, manifestVersion string
		wantErr                                   bool
	}{
		{"valid", "1.0.0", "2.0.0", "2.0.0", false},
		{"equal rollback", "2.0.0", "2.0.0", "2.0.0", true},
		{"older rollback", "3.0.0", "2.0.0", "2.0.0", true},
		{"different requested version", "1.0.0", "2.0.0", "2.0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := testManifest(tc.manifestVersion, name, body)
			_, err := parseReleaseManifest(manifest, tc.current, tc.requested, name, time.Now())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestReleaseURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		raw, kind string
		testHTTP  bool
		want      bool
	}{
		{"https://api.github.com/repos/o/r/releases/latest", "api", false, true},
		{"http://api.github.com/repos/o/r/releases/latest", "api", false, false},
		{"https://evil.example/file", "asset", false, false},
		{"https://github.com/o/r/releases/download/v2/file", "asset", false, true},
		{"https://release-assets.githubusercontent.com/file", "asset", false, true},
		{"http://127.0.0.1:1234/file", "asset", true, true},
		{"http://evil.example/file", "asset", true, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := releaseURLAllowed(tc.raw, tc.kind, tc.testHTTP); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
