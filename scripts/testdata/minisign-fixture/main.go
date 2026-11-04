// Command minisign-fixture implements the legacy Ed subset used by offline tests.
// Production scripts must use the real minisign executable.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	generate := flag.Bool("G", false, "generate")
	sign := flag.Bool("S", false, "sign")
	verify := flag.Bool("V", false, "verify")
	secretPath := flag.String("s", "", "secret key")
	publicPath := flag.String("p", "", "public key file")
	publicText := flag.String("P", "", "public key")
	messagePath := flag.String("m", "", "message")
	signaturePath := flag.String("x", "", "signature")
	trusted := flag.String("t", "fixture", "trusted comment")
	_ = flag.String("c", "", "untrusted comment")
	_ = flag.Bool("W", false, "unencrypted fixture key")
	_ = flag.Bool("l", false, "legacy Ed")
	_ = flag.Bool("q", false, "quiet")
	_ = flag.Bool("H", false, "prehashed only")
	flag.Parse()
	var err error
	switch {
	case *generate:
		err = generateKey(*secretPath, *publicPath)
	case *sign:
		err = signFile(*secretPath, *messagePath, *signaturePath, *trusted)
	case *verify:
		err = verifyFile(*publicPath, *publicText, *messagePath, *signaturePath)
	default:
		err = errors.New("expected -G, -S or -V")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generateKey(secretPath, publicPath string) error {
	if secretPath == "" || publicPath == "" {
		return errors.New("fixture key paths are required")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	seed := privateKey.Seed()
	if err := os.WriteFile(secretPath, []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return err
	}
	packet := publicPacket(publicKey)
	text := "untrusted comment: minisign fixture public key\n" + base64.StdEncoding.EncodeToString(packet) + "\n"
	return os.WriteFile(publicPath, []byte(text), 0o644)
}

func signFile(secretPath, messagePath, signaturePath, trusted string) error {
	seedText, err := os.ReadFile(secretPath)
	if err != nil {
		return err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(seedText)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("invalid fixture secret key")
	}
	message, err := os.ReadFile(messagePath)
	if err != nil {
		return err
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	messageSignature := ed25519.Sign(privateKey, message)
	packet := append(append([]byte(nil), publicPacket(publicKey)[:10]...), messageSignature...)
	globalMessage := append(append([]byte(nil), messageSignature...), trusted...)
	global := ed25519.Sign(privateKey, globalMessage)
	if signaturePath == "" {
		signaturePath = messagePath + ".minisig"
	}
	text := "untrusted comment: signature from minisign fixture key\n" +
		base64.StdEncoding.EncodeToString(packet) + "\ntrusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"
	return os.WriteFile(signaturePath, []byte(text), 0o644)
}

func verifyFile(publicPath, publicText, messagePath, signaturePath string) error {
	if publicText == "" {
		body, err := os.ReadFile(publicPath)
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		publicText = lines[len(lines)-1]
	}
	publicPacketBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicText))
	if err != nil || len(publicPacketBytes) != 42 || string(publicPacketBytes[:2]) != "Ed" {
		return errors.New("invalid fixture public key")
	}
	message, err := os.ReadFile(messagePath)
	if err != nil {
		return err
	}
	if signaturePath == "" {
		signaturePath = messagePath + ".minisig"
	}
	body, err := os.ReadFile(signaturePath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return errors.New("invalid fixture signature")
	}
	signaturePacket, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(signaturePacket) != 74 || string(signaturePacket[:2]) != "Ed" || !equal(signaturePacket[2:10], publicPacketBytes[2:10]) {
		return errors.New("invalid fixture signature packet")
	}
	publicKey := ed25519.PublicKey(publicPacketBytes[10:])
	messageSignature := signaturePacket[10:]
	if !ed25519.Verify(publicKey, message, messageSignature) {
		return errors.New("fixture message signature verification failed")
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	trusted := strings.TrimPrefix(lines[2], "trusted comment: ")
	if err != nil || !ed25519.Verify(publicKey, append(append([]byte(nil), messageSignature...), trusted...), global) {
		return errors.New("fixture global signature verification failed")
	}
	return nil
}

func publicPacket(publicKey ed25519.PublicKey) []byte {
	digest := sha256.Sum256(publicKey)
	packet := append([]byte("Ed"), digest[:8]...)
	return append(packet, publicKey...)
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var difference byte
	for i := range a {
		difference |= a[i] ^ b[i]
	}
	return difference == 0
}
