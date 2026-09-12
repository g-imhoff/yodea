// Package sites unpacks uploaded Vite dist archives into per-preview
// directories and derives DNS-safe preview labels.
//
// Security model: uploaded code is static data only, never executed by the
// server. ExtractDist rejects absolute paths, ".." escapes, symlinks,
// hardlinks, device nodes, dotfiles, and oversized payloads before touching
// the destination, and refuses archives without a top-level index.html.
// Deploys swap atomically so readers never see a half-written preview.
//
// Run-safe note: in production the container runs as an unprivileged user
// and only the data-only sites directory (DataDir/sites) is writable; the
// code here additionally writes files 0644 / dirs 0755 with no exec bits.
package sites

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Cheap abuse guards for the MVP (not capacity planning): tarbombs of
// highly compressible files can expand orders of magnitude, so the expanded
// cap is what matters.
const (
	MaxUploadBytes     = 30 << 20 // 30MB compressed per deploy
	MaxExpandedBytes   = 150 << 20
	MaxFiles           = 20000
	MaxSingleFileBytes = 25 << 20
)

// ValidateLabel enforces DNS label rules for preview subdomains.
func ValidateLabel(label string) error {
	if len(label) < 1 || len(label) > 63 {
		return fmt.Errorf("label must be 1-63 chars, got %d", len(label))
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return errors.New("label must not start or end with a hyphen")
	}
	for _, r := range label {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return fmt.Errorf("label allows only a-z, 0-9, hyphen: %q", label)
	}
	return nil
}

// Sanitize maps free text to a DNS-safe part.
func Sanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(collapse(b.String()), "-")
	if out == "" {
		out = "site"
	}
	return out
}

// LabelFor builds <userpart>-<project> within 63 chars. The user part is
// preserved in full so distinct users keep distinct labels; only the
// project tail is trimmed to fit. Existing short labels are unchanged.
func LabelFor(userPart, project string) string {
	p := Sanitize(project)
	u := Sanitize(userPart)
	label := u + "-" + p
	if len(label) > 63 {
		keep := 63 - len(u) - 1
		if keep < 8 {
			// user part wins for uniqueness; hard-trim project.
			if len(p) > 8 {
				p = p[:8]
			}
			label = u + "-" + p
			if len(label) > 63 {
				label = label[:63]
			}
		} else {
			label = u + "-" + p[:keep]
		}
	}
	return strings.Trim(label, "-")
}

func collapse(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if !prevDash {
				b.WriteRune(r)
			}
			prevDash = true
			continue
		}
		prevDash = false
		b.WriteRune(r)
	}
	return b.String()
}

// CheckProjectName is the single source of truth for project-name
// validation: single path segment, bounded, no leading/trailing hyphen,
// plus blank/dot-only and reserved-fallback rejection (names sanitizing
// to "site" unless exactly "site"). Control characters are rejected (they
// break tab-separated list output and enable terminal line injection) as
// is leading/trailing whitespace. Both the CLI (client.CheckProject)
// and the server (validProject) delegate here so a server-side tightening
// cannot silently break old CLIs at deploy time.
func CheckProjectName(raw string) error {
	if raw == "" || len(raw) > 40 {
		return fmt.Errorf("bad project name %q: must be 1-40 chars", raw)
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("bad project name %q: must not be blank", raw)
	}
	if raw != strings.TrimSpace(raw) {
		return fmt.Errorf("bad project name %q: must not have leading or trailing whitespace", raw)
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return fmt.Errorf("bad project name %q: must not contain control characters", raw)
		}
	}
	if strings.Trim(raw, ".") == "" {
		return fmt.Errorf("bad project name %q: must not be dot-only", raw)
	}
	if strings.HasPrefix(raw, "-") || strings.HasSuffix(raw, "-") {
		return fmt.Errorf("bad project name %q: must not start or end with a hyphen", raw)
	}
	if strings.ContainsAny(raw, "/\\?#") {
		return fmt.Errorf("bad project name %q: must be a single path segment (no / \\ ? #)", raw)
	}
	if Sanitize(raw) == "site" && raw != "site" {
		return fmt.Errorf("bad project name %q: resolves to reserved name %q", raw, "site")
	}
	return nil
}

// validationError marks input-validation failures (400). Infra/IO failures
// (MkdirTemp, EACCES, ENOSPC, rename errors) are returned unwrapped so
// callers map them to 500.
type validationError struct {
	msg   string
	cause error
}

func (e *validationError) Error() string {
	if e.cause != nil {
		return e.msg + ": " + e.cause.Error()
	}
	return e.msg
}

func (e *validationError) Unwrap() error { return e.cause }

func validationf(format string, args ...any) error {
	return &validationError{msg: fmt.Sprintf(format, args...)}
}

func validationWrap(cause error, format string, args ...any) error {
	return &validationError{msg: fmt.Sprintf(format, args...), cause: cause}
}

// IsValidationError reports whether err is an input-validation failure
// (400). Anything else from extraction is infra/IO (500).
func IsValidationError(err error) bool {
	var ve *validationError
	return errors.As(err, &ve)
}

func isDiskError(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return true
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return true
	}
	var se *os.SyscallError
	if errors.As(err, &se) {
		return true
	}
	return false
}

// SiteDir centralizes DataDir/sites/<label> path construction. All server
// handlers must use this (plus ReplaceSite/RemoveSite/NewStagingDir) instead
// of building the path by hand.
func SiteDir(dataDir, label string) string {
	return filepath.Join(dataDir, "sites", label)
}

func sitesRoot(dataDir string) string {
	return filepath.Join(dataDir, "sites")
}

// NewStagingDir creates a unique staging dir under DataDir/sites for one
// deploy. Each deploy gets its own staging dir so concurrent deploys for one
// label never share a path.
func NewStagingDir(dataDir string) (string, error) {
	root := sitesRoot(dataDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, ".stage-*")
}

// RemoveSite deletes a label's live files. It never touches staging dirs.
func RemoveSite(dataDir, label string) error {
	return os.RemoveAll(SiteDir(dataDir, label))
}

// ReplaceSite atomically swaps a staging dir into place as the label's live
// files. The previous live dir (if any) moves aside to a unique backup path
// so concurrent replaces never share a .old path.
func ReplaceSite(dataDir, label, staging string) error {
	return swapDir(SiteDir(dataDir, label), staging)
}

func swapDir(dest, staging string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err != nil {
		if os.IsNotExist(err) {
			if err := os.Rename(staging, dest); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	parent := filepath.Dir(dest)
	tmp, err := os.MkdirTemp(parent, ".old-*")
	if err != nil {
		return err
	}
	if err := os.Remove(tmp); err != nil {
		return err
	}
	backup := tmp
	if err := os.Rename(dest, backup); err != nil {
		return err
	}
	if err := os.Rename(staging, dest); err != nil {
		// Roll back on failure.
		_ = os.Rename(backup, dest)
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}

// ExtractResult summarizes a deploy.
type ExtractResult struct {
	Files int
	Bytes int64
}

// ExtractDist safely unpacks a gzipped tar of a Vite dist/ into dest,
// which is replaced atomically. It rejects absolute paths, ".." escapes,
// symlinks, hardlinks, device nodes, dotfiles, oversized payloads, and
// archives without a top-level index.html.
func ExtractDist(tarGz io.Reader, dest string, maxExpanded int64, maxFiles int) (*ExtractResult, error) {
	if maxExpanded <= 0 {
		maxExpanded = MaxExpandedBytes
	}
	if maxFiles <= 0 {
		maxFiles = MaxFiles
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dest), ".stage-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)

	res, err := ExtractToStaging(tarGz, staging, maxExpanded, maxFiles)
	if err != nil {
		return nil, err
	}
	if err := swapDir(dest, staging); err != nil {
		return nil, err
	}
	return res, nil
}

// ExtractToStaging unpacks a gzipped tar into an existing staging dir
// without touching the live destination. Callers Upsert metadata first, then
// ReplaceSite to swap staging live. Validation failures return an error for
// which IsValidationError is true (400); infra/IO failures return raw errors
// (500).
func ExtractToStaging(tarGz io.Reader, staging string, maxExpanded int64, maxFiles int) (*ExtractResult, error) {
	if maxExpanded <= 0 {
		maxExpanded = MaxExpandedBytes
	}
	if maxFiles <= 0 {
		maxFiles = MaxFiles
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, err
	}

	gz, err := gzip.NewReader(tarGz)
	if err != nil {
		return nil, validationWrap(err, "bad gzip")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	res := &ExtractResult{}
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, validationWrap(err, "bad tar")
		}
		name := filepath.ToSlash(hdr.Name)
		name = strings.TrimPrefix(name, "./")
		if name == "" || name == "." {
			continue
		}
		// Reject absolute paths and escapes before cleaning.
		if filepath.IsAbs(hdr.Name) || strings.HasPrefix(name, "/") {
			return nil, validationf("rejected absolute path %q", hdr.Name)
		}
		clean := filepath.Clean(name)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, validationf("rejected escape %q", hdr.Name)
		}
		// Dotfiles and dot-directories are rejected: the brief requires
		// deploys to REJECT dotfiles (400) rather than silently skipping,
		// and dist/ never needs them; they hide secrets like .env that
		// must not be served.
		for _, part := range strings.Split(clean, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				return nil, validationf("rejected dotfile %q", hdr.Name)
			}
		}
		target := filepath.Join(staging, clean)
		if !strings.HasPrefix(target, staging+string(filepath.Separator)) && target != staging {
			return nil, validationf("rejected escape %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size > MaxSingleFileBytes {
				return nil, validationf("file %q exceeds %d bytes", clean, MaxSingleFileBytes)
			}
			res.Files++
			if res.Files > maxFiles {
				return nil, validationf("archive exceeds %d files", maxFiles)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return nil, err
			}
			n, err := io.Copy(f, io.LimitReader(tr, maxExpanded-total+1))
			f.Close()
			if err != nil {
				if isDiskError(err) {
					return nil, err
				}
				return nil, validationWrap(err, "bad tar data for %q", clean)
			}
			total += n
			res.Bytes += n
			if total > maxExpanded {
				return nil, validationf("archive expands past %d bytes", maxExpanded)
			}
		default:
			// Symlinks, hardlinks, devices, fifos: rejected. They enable
			// path traversal and cross-user reads on shared hosting.
			return nil, validationf("rejected non-regular entry %q (type %c)", hdr.Name, hdr.Typeflag)
		}
	}
	if _, err := os.Stat(filepath.Join(staging, "index.html")); err != nil {
		if os.IsNotExist(err) {
			return nil, validationf("archive must contain a top-level index.html")
		}
		return nil, err
	}
	return res, nil
}
