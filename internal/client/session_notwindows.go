//go:build !windows

package client

import "os"

// restrictSessionFile enforces owner-only access on platforms where unix
// permission bits are authoritative.
func restrictSessionFile(path string) error {
	return os.Chmod(path, 0o600)
}
