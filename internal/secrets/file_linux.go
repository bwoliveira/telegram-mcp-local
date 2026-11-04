//go:build linux

package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxFileSize = 32 << 20
const fileVersion = "TGMCP1"

type fileStore struct {
	namespace                   string
	dataDir, keyDir             string
	dataPath, keyPath, lockPath string
	fallback                    bool
}

func (*fileStore) Backend() Backend   { return File }
func (s *fileStore) Fallback() bool   { return s.fallback }
func (*fileStore) String() string     { return "secrets.Store(file; redacted)" }
func (s *fileStore) GoString() string { return s.String() }

func newFileStore(namespace string, o Options, fallback bool) (Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, ErrStorage
	}
	if o.DataDir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
		if !filepath.IsAbs(base) {
			return nil, ErrInvalid
		}
		o.DataDir = filepath.Join(base, "telegram-mcp", "secrets")
	}
	if o.KeyDir == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		if !filepath.IsAbs(base) {
			return nil, ErrInvalid
		}
		o.KeyDir = filepath.Join(base, "telegram-mcp", "keys")
	}
	if !filepath.IsAbs(o.DataDir) || !filepath.IsAbs(o.KeyDir) {
		return nil, ErrInvalid
	}
	dataDir := filepath.Clean(o.DataDir)
	keyDir := filepath.Clean(o.KeyDir)
	if within(dataDir, keyDir) || within(keyDir, dataDir) {
		return nil, ErrInvalid
	}
	s := &fileStore{namespace: namespace, dataDir: dataDir, keyDir: keyDir, fallback: fallback}
	s.dataPath = filepath.Join(dataDir, namespace+".enc")
	s.keyPath = filepath.Join(keyDir, namespace+".key")
	s.lockPath = filepath.Join(dataDir, namespace+".lock")
	err = s.locked(func() error { _, _, err := s.load(); return err })
	if err != nil {
		return nil, err
	}
	return s, nil
}
func within(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func secureDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return ErrStorage
	}
	info, err := os.Lstat(path)
	if err != nil {
		return ErrStorage
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return ErrPermission
	}
	// Reject symlinked parents too, which could collapse the separate locations.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ErrStorage
	}
	if resolved != path {
		return ErrPermission
	}
	return nil
}
func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
func checkFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return ErrStorage
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return ErrStorage
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return ErrPermission
	}
	return nil
}
func secureOpen(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EACCES) {
			return nil, ErrPermission
		}
		return nil, ErrStorage
	}
	f := os.NewFile(uintptr(fd), "secrets")
	if err := checkFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func readPrivate(path string) ([]byte, error) {
	f, err := secureOpen(path, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, ErrStorage
	}
	if len(b) > maxFileSize {
		return nil, ErrCorrupt
	}
	return b, nil
}
func (s *fileStore) locked(fn func() error) error {
	if err := secureDir(s.dataDir); err != nil {
		return err
	}
	if err := secureDir(s.keyDir); err != nil {
		return err
	}
	lock, err := secureOpen(s.lockPath, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return ErrStorage
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return fn()
}
func (s *fileStore) aad() []byte { return []byte(fileVersion + ":" + s.namespace) }
func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrCorrupt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCorrupt
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrCorrupt
	}
	return a, nil
}
func (s *fileStore) load() (map[Key][]byte, []byte, error) {
	data, err := readPrivate(s.dataPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	missingData := errors.Is(err, os.ErrNotExist)
	key, err := readPrivate(s.keyPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if !missingData && errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrCorrupt
	}
	if key != nil && len(key) != 32 {
		return nil, nil, ErrCorrupt
	}
	if missingData {
		return map[Key][]byte{}, key, nil
	}
	a, err := aead(key)
	if err != nil {
		return nil, nil, err
	}
	header := len(fileVersion)
	n := a.NonceSize()
	if len(data) < header+n+a.Overhead() || string(data[:header]) != fileVersion {
		return nil, nil, ErrCorrupt
	}
	plain, err := a.Open(nil, data[header:header+n], data[header+n:], s.aad())
	if err != nil {
		return nil, nil, ErrCorrupt
	}
	var values map[Key][]byte
	if json.Unmarshal(plain, &values) != nil || values == nil {
		return nil, nil, ErrCorrupt
	}
	for k := range values {
		if !validKey(k) {
			return nil, nil, ErrCorrupt
		}
	}
	return values, key, nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return ErrStorage
	}
	defer f.Close()
	if f.Sync() != nil {
		return ErrStorage
	}
	return nil
}
func atomicPrivate(path string, data []byte) error {
	// Existing files must satisfy the same policy before replacing them.
	existing, err := secureOpen(path, unix.O_RDONLY)
	if err == nil {
		existing.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".secrets-*")
	if err != nil {
		return ErrStorage
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return ErrStorage
	}
	if err := tmp.Sync(); err != nil {
		return ErrStorage
	}
	if err := tmp.Close(); err != nil {
		return ErrStorage
	}
	if err := os.Rename(name, path); err != nil {
		return ErrStorage
	}
	return syncDir(dir)
}
func (s *fileStore) save(values map[Key][]byte, key []byte) error {
	if key == nil {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return ErrStorage
		}
		if err := atomicPrivate(s.keyPath, key); err != nil {
			return err
		}
	}
	a, err := aead(key)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return ErrStorage
	}
	if len(plain) > maxFileSize-len(fileVersion)-a.NonceSize()-a.Overhead() {
		return ErrTooLarge
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ErrStorage
	}
	data := append([]byte(fileVersion), nonce...)
	data = a.Seal(data, nonce, plain, s.aad())
	return atomicPrivate(s.dataPath, data)
}
func (s *fileStore) Get(k Key) ([]byte, error) {
	if !validKey(k) {
		return nil, ErrInvalid
	}
	var value []byte
	err := s.locked(func() error {
		m, _, err := s.load()
		if err != nil {
			return err
		}
		v, ok := m[k]
		if !ok {
			return ErrNotFound
		}
		value = v
		return nil
	})
	return value, err
}
func (s *fileStore) Set(k Key, v []byte) error {
	if !validKey(k) {
		return ErrInvalid
	}
	if len(v) > maxFileSize/2 {
		return ErrTooLarge
	}
	return s.locked(func() error {
		m, key, err := s.load()
		if err != nil {
			return err
		}
		m[k] = v
		return s.save(m, key)
	})
}
func (s *fileStore) Delete(k Key) error {
	if !validKey(k) {
		return ErrInvalid
	}
	return s.locked(func() error {
		m, key, err := s.load()
		if err != nil {
			return err
		}
		if _, ok := m[k]; !ok {
			return nil
		}
		delete(m, k)
		return s.save(m, key)
	})
}
func (s *fileStore) DeleteAll() error {
	return s.locked(func() error {
		var result error
		// No decryption required: damaged ciphertext must not block account removal.
		for _, p := range []string{s.dataPath, s.keyPath} {
			f, err := secureOpen(p, unix.O_RDONLY)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				result = err
				continue
			}
			f.Close()
			if os.Remove(p) != nil {
				result = ErrStorage
			}
		}
		if err := syncDir(s.dataDir); err != nil {
			result = err
		}
		if err := syncDir(s.keyDir); err != nil {
			result = err
		}
		return result
	})
}
