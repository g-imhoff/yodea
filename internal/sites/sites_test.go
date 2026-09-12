package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
	flag byte
}

func pack(t *testing.T, entries []entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		flag := e.flag
		if flag == 0 {
			flag = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: flag, Format: tar.FormatPAX}
		if flag == tar.TypeSymlink || flag == tar.TypeLink {
			hdr.Linkname = "index.html"
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestExtractDistRoundTrip(t *testing.T) {
	arc := pack(t, []entry{
		{name: "index.html", body: "<h1>hi</h1>"},
		{name: "assets/app.js", body: "console.log(1)"},
	})
	dest := filepath.Join(t.TempDir(), "sites", "lbl")
	res, err := ExtractDist(arc, dest, 0, 0)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Files != 2 {
		t.Fatalf("files = %d, want 2", res.Files)
	}
	if _, err := os.Stat(filepath.Join(dest, "index.html")); err != nil {
		t.Fatalf("index.html missing: %v", err)
	}
}

func TestExtractDistRequiresIndexHTML(t *testing.T) {
	arc := pack(t, []entry{{name: "assets/app.js", body: "x"}})
	_, err := ExtractDist(arc, filepath.Join(t.TempDir(), "lbl"), 0, 0)
	if err == nil || !strings.Contains(err.Error(), "index.html") {
		t.Fatalf("want index.html error, got %v", err)
	}
}

func TestExtractDistRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../evil.html", "a/../../evil.html", "/abs.html"} {
		arc := pack(t, []entry{{name: name, body: "x"}, {name: "index.html", body: "ok"}})
		_, err := ExtractDist(arc, filepath.Join(t.TempDir(), "lbl"), 0, 0)
		if err == nil {
			t.Fatalf("%q: want rejection, got nil", name)
		}
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "..", "evil.html")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside dest")
	}
}

func TestExtractDistRejectsSymlinks(t *testing.T) {
	arc := pack(t, []entry{
		{name: "link", body: "", flag: tar.TypeSymlink},
		{name: "index.html", body: "ok"},
	})
	_, err := ExtractDist(arc, filepath.Join(t.TempDir(), "lbl"), 0, 0)
	if err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("want non-regular rejection, got %v", err)
	}
}

func TestExtractDistRejectsDotfiles(t *testing.T) {
	arc := pack(t, []entry{{name: "index.html", body: "ok"}, {name: ".env", body: "secret"}})
	_, err := ExtractDist(arc, filepath.Join(t.TempDir(), "lbl"), 0, 0)
	if err == nil {
		t.Fatal("want dotfile rejection, got nil")
	}
	arc2 := pack(t, []entry{{name: "index.html", body: "ok"}, {name: ".git/config", body: "x"}})
	_, err = ExtractDist(arc2, filepath.Join(t.TempDir(), "lbl2"), 0, 0)
	if err == nil {
		t.Fatal("want dot-directory rejection, got nil")
	}
}

func TestExtractDistRejectsDevicesAndFifos(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag byte
	}{
		{"devnode", tar.TypeChar},
		{"blocknode", tar.TypeBlock},
		{"apipe", tar.TypeFifo},
	} {
		arc := pack(t, []entry{{name: "index.html", body: "ok"}, {name: tc.name, body: "", flag: tc.flag}})
		_, err := ExtractDist(arc, filepath.Join(t.TempDir(), "lbl"), 0, 0)
		if err == nil || !strings.Contains(err.Error(), "non-regular") {
			t.Fatalf("%s (type %c): want non-regular rejection, got %v", tc.name, tc.flag, err)
		}
	}
}

func TestValidateLabel(t *testing.T) {
	for _, bad := range []string{"", "-abc", "abc-", "ABC", "a_b", "a.b", strings.Repeat("a", 64)} {
		if err := ValidateLabel(bad); err == nil {
			t.Fatalf("%q: want error, got nil", bad)
		}
	}
	for _, ok := range []string{"a", "abc-123", strings.Repeat("a", 63)} {
		if err := ValidateLabel(ok); err != nil {
			t.Fatalf("%q: want nil, got %v", ok, err)
		}
	}
}

func TestLabelForFits63(t *testing.T) {
	lbl := LabelFor("someuser", strings.Repeat("p", 100))
	if len(lbl) > 63 {
		t.Fatalf("label too long: %d", len(lbl))
	}
	if err := ValidateLabel(lbl); err != nil {
		t.Fatalf("label invalid: %v", err)
	}
}

func TestLabelForKeepsCommonShape(t *testing.T) {
	if got := LabelFor("alice", "blog"); got != "alice-blog" {
		t.Fatalf("common shape = %q, want %q", got, "alice-blog")
	}
}

func TestLabelForDistinguishesUUIDLikeUsers(t *testing.T) {
	// Simulate post-UserPart handles: same 12-char prefix, distinct by 20.
	u1 := "123e4567-e89b-12d3-a"
	u2 := "123e4567-e89c-12d3-a"
	l1 := LabelFor(u1, "blog")
	l2 := LabelFor(u2, "blog")
	if l1 == l2 {
		t.Fatalf("UUID-like users collide: %q for %q vs %q", l1, u1, u2)
	}
}

func TestLabelForLongProjectDistinctUsers(t *testing.T) {
	for _, project := range []string{strings.Repeat("p", 40), strings.Repeat("p", 100)} {
		l1 := LabelFor("dev-alice-long-x", project)
		l2 := LabelFor("dev-alice-long-y", project)
		if l1 == l2 {
			t.Fatalf("project len %d: distinct users collapse to %q", len(project), l1)
		}
		for _, lbl := range []string{l1, l2} {
			if len(lbl) > 63 {
				t.Fatalf("label too long (%d): %q", len(lbl), lbl)
			}
			if err := ValidateLabel(lbl); err != nil {
				t.Fatalf("label invalid: %q: %v", lbl, err)
			}
		}
	}
}
