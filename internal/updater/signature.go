package updater

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ReleasePublicKey is replaced at release build time with the base64 line from
// the publisher's minisign public key. The placeholder deliberately fails
// closed in development builds.
var ReleasePublicKey = releaseKeyPlaceholder

const maxSignatureSize = 16 << 10

func verifyMinisign(message, signature []byte, publicText string) error {
	keyID, publicKey, err := parseMinisignPublicKey(publicText)
	if err != nil {
		return fmt.Errorf("invalid release signing public key: %w", err)
	}
	if len(signature) == 0 || len(signature) > maxSignatureSize {
		return errors.New("missing or oversized release signature")
	}
	text := strings.ReplaceAll(string(signature), "\r\n", "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "untrusted comment: ") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return errors.New("malformed minisign signature")
	}
	raw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(raw) != 2+8+ed25519.SignatureSize {
		return errors.New("malformed minisign message signature")
	}
	// Legacy Ed signs the original bytes. ED requires BLAKE2b-512 and is not
	// accepted because the updater intentionally uses only the Go standard library.
	if string(raw[:2]) != "Ed" {
		return errors.New("unsupported minisign algorithm (release must use legacy Ed mode)")
	}
	if !equalBytes(raw[2:10], keyID) {
		return errors.New("release signature was made by a different key")
	}
	messageSignature := raw[10:]
	if !ed25519.Verify(publicKey, message, messageSignature) {
		return errors.New("release manifest signature verification failed")
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(global) != ed25519.SignatureSize {
		return errors.New("malformed minisign global signature")
	}
	trustedComment := strings.TrimPrefix(lines[2], "trusted comment: ")
	globalMessage := make([]byte, 0, len(messageSignature)+len(trustedComment))
	globalMessage = append(globalMessage, messageSignature...)
	globalMessage = append(globalMessage, trustedComment...)
	if !ed25519.Verify(publicKey, globalMessage, global) {
		return errors.New("minisign trusted comment verification failed")
	}
	return nil
}

func parseMinisignPublicKey(text string) ([]byte, ed25519.PublicKey, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if strings.Contains(text, "\n") {
		lines := strings.Split(text, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "untrusted comment: ") {
			return nil, nil, errors.New("malformed public key file")
		}
		text = lines[1]
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(raw) != 2+8+ed25519.PublicKeySize || string(raw[:2]) != "Ed" {
		return nil, nil, errors.New("expected a minisign Ed public key")
	}
	keyID := append([]byte(nil), raw[2:10]...)
	publicKey := append(ed25519.PublicKey(nil), raw[10:]...)
	return keyID, publicKey, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}

func parseReleaseManifest(body []byte, current, requested, assetName string, now time.Time) (string, error) {
	if !newer(requested, current) {
		return "", fmt.Errorf("refusing release version %s because it is not newer than %s", requested, current)
	}
	var version, commit, date, digest string
	seenFiles := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "# version: "):
			if version != "" {
				return "", errors.New("release manifest has duplicate version metadata")
			}
			version = strings.TrimPrefix(line, "# version: ")
		case strings.HasPrefix(line, "# source_commit: "):
			if commit != "" {
				return "", errors.New("release manifest has duplicate source commit metadata")
			}
			commit = strings.TrimPrefix(line, "# source_commit: ")
		case strings.HasPrefix(line, "# date: "):
			if date != "" {
				return "", errors.New("release manifest has duplicate date metadata")
			}
			date = strings.TrimPrefix(line, "# date: ")
		case strings.HasPrefix(line, "#"):
			return "", fmt.Errorf("release manifest has unknown metadata: %s", line)
		case line == "":
			return "", errors.New("release manifest contains an empty line")
		default:
			fields := strings.Fields(line)
			if len(fields) != 2 {
				return "", errors.New("release manifest has an invalid file entry")
			}
			name := strings.TrimPrefix(fields[1], "*")
			decoded, err := hex.DecodeString(fields[0])
			if err != nil || len(decoded) != sha256.Size || seenFiles[name] {
				return "", fmt.Errorf("release manifest has an invalid or duplicate SHA-256 for %s", name)
			}
			seenFiles[name] = true
			if name == assetName {
				digest = strings.ToLower(fields[0])
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read release manifest: %w", err)
	}
	requested = strings.TrimPrefix(requested, "v")
	if version != requested {
		return "", fmt.Errorf("signed manifest version %q does not match requested release %q", version, requested)
	}
	if len(commit) != 40 {
		return "", errors.New("signed manifest has an invalid source commit")
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return "", errors.New("signed manifest has an invalid source commit")
	}
	published, err := time.Parse(time.RFC3339, date)
	if err != nil || published.Location() != time.UTC {
		return "", errors.New("signed manifest has an invalid UTC release date")
	}
	if published.After(now.Add(24 * time.Hour)) {
		return "", errors.New("signed manifest release date is unreasonably far in the future")
	}
	if digest == "" {
		return "", fmt.Errorf("signed manifest does not have %s", assetName)
	}
	return digest, nil
}

func releaseURLAllowed(rawURL, kind string, testHTTP bool) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Fragment != "" || u.Hostname() == "" || kind == "api" && u.RawQuery != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if testHTTP && u.Scheme == "http" && isLoopbackHost(host) {
		return true
	}
	if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	if kind == "api" {
		return host == "api.github.com"
	}
	switch host {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
