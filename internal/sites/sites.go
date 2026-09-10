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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// LabelFor builds <userpart>-<project> within 63 chars.
func LabelFor(userPart, project string) string {
	p := Sanitize(project)
	u := Sanitize(userPart)
	label := u + "-" + p
	if len(label) > 63 {
		keep := 63 - len(u) - 1
		if keep < 8 {
			// user part wins for uniqueness; hard-trim project.
			p = p[:8]
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

	gz, err := gzip.NewReader(tarGz)
	if err != nil {
		return nil, fmt.Errorf("bad gzip: %w", err)
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
			return nil, fmt.Errorf("bad tar: %w", err)
		}
		name := filepath.ToSlash(hdr.Name)
		name = strings.TrimPrefix(name, "./")
		if name == "" || name == "." {
			continue
		}
		// Reject absolute paths and escapes before cleaning.
		if filepath.IsAbs(hdr.Name) || strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("rejected absolute path %q", hdr.Name)
		}
		clean := filepath.Clean(name)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("rejected escape %q", hdr.Name)
		}
		// Dotfiles and dot-directories are rejected: the brief requires
		// deploys to REJECT dotfiles (400) rather than silently skipping,
		// and dist/ never needs them; they hide secrets like .env that
		// must not be served.
		for _, part := range strings.Split(clean, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				return nil, fmt.Errorf("rejected dotfile %q", hdr.Name)
			}
		}
		target := filepath.Join(staging, clean)
		if !strings.HasPrefix(target, staging+string(filepath.Separator)) && target != staging {
			return nil, fmt.Errorf("rejected escape %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size > MaxSingleFileBytes {
				return nil, fmt.Errorf("file %q exceeds %d bytes", clean, MaxSingleFileBytes)
			}
			res.Files++
			if res.Files > maxFiles {
				return nil, fmt.Errorf("archive exceeds %d files", maxFiles)
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
				return nil, err
			}
			total += n
			res.Bytes += n
			if total > maxExpanded {
				return nil, fmt.Errorf("archive expands past %d bytes", maxExpanded)
			}
		default:
			// Symlinks, hardlinks, devices, fifos: rejected. They enable
			// path traversal and cross-user reads on shared hosting.
			return nil, fmt.Errorf("rejected non-regular entry %q (type %c)", hdr.Name, hdr.Typeflag)
		}
	}
	if _, err := os.Stat(filepath.Join(staging, "index.html")); err != nil {
		return nil, errors.New("archive must contain a top-level index.html")
	}
	// Atomic swap: move old aside, move staging in, drop old.
	old := dest + ".old"
	os.RemoveAll(old)
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			return nil, err
		}
		defer os.RemoveAll(old)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, dest); err != nil {
		// Roll back on failure.
		if _, err2 := os.Stat(old); err2 == nil {
			_ = os.Rename(old, dest)
		}
		return nil, err
	}
	os.RemoveAll(old)
	return res, nil
}

// PackDir creates a gzipped tar of srcDir for upload. It includes regular
// files only, skips dotfiles to match ExtractDist, and skips node_modules.
func PackDir(srcDir string, w io.Writer) (int, error) {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	count := 0
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && base == "node_modules" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular file %q", rel)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:    filepath.ToSlash(rel),
			Mode:    0o644,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = f.Close()
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		count++
		if count > MaxFiles {
			return fmt.Errorf("directory exceeds %d files", MaxFiles)
		}
		return nil
	})
	return count, err
}
