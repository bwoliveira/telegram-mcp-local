//go:build !linux

package secrets

// The encrypted fallback requires POSIX permissions and is Linux-only.
func newFileStore(_ string, _ Options, _ bool) (Store, error) {
	return nil, ErrInvalid
}
