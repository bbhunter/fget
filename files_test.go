package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPortableNames(t *testing.T) {
	for raw, want := range map[string]string{
		"": "index.html", ".": "index.html", "..": "index.html", "CON": "_CON", "nul.txt": "_nul.txt",
		"COM1.log": "_COM1.log", "LPT².txt": "_LPT².txt", "con .txt": "_con .txt",
		"a:b?.txt": "a_b_.txt", "a\\b/c": "a_b_c", "file. ": "file", "normal.js": "normal.js",
	} {
		if got := safeComponent(raw); got != want {
			t.Errorf("%q: got %q, want %q", raw, got, want)
		}
	}
	long := safeComponent(strings.Repeat("界", 100))
	if len(long) > 100 || !utf8.ValidString(long) {
		t.Fatal("invalid truncated filename")
	}
	for _, raw := range []string{"https://example.com", "https://example.com/dir/"} {
		u, _ := url.Parse(raw)
		if outputName(u, false) != "index.html" {
			t.Fatal("missing default filename")
		}
	}
}

func TestUniqueNames(t *testing.T) {
	seen := map[string]bool{}
	for _, raw := range []string{"https://a.example/x/file.js", "https://a.example/y/file.js", "https://a.example/x/file.js?v=2", "https://b.example/x/file.js"} {
		u, _ := url.Parse(raw)
		name := outputName(u, true)
		if seen[name] {
			t.Fatalf("collision: %s", name)
		}
		seen[name] = true
		if name != outputName(u, true) {
			t.Fatal("unstable name")
		}
		u.Fragment = "section"
		if name != outputName(u, true) {
			t.Fatal("fragment changed filename")
		}
	}
}

func TestOutputLayout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	for _, flat := range []bool{false, true} {
		cfg := testConfig(t)
		cfg.noFolders = flat
		file, _, err := testDownload(t, context.Background(), cfg, server.URL+"/nested/file.js")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(cfg.output, "file.js")
		if !flat {
			want = filepath.Join(cfg.output, "results", safeComponent(u.Host), "file.js")
		}
		if file != want {
			t.Fatalf("got %s, want %s", file, want)
		}
	}
}

func TestOutputRootRejectsEscapingSymlink(t *testing.T) {
	cfg := testConfig(t)
	cfg.noFolders = false
	if err := os.MkdirAll(cfg.outputRoot(), 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cfg.outputRoot(), "example.com")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := testDownload(t, context.Background(), cfg, "https://example.com/file"); err == nil {
		t.Fatal("escaping output root accepted")
	}
	assertEmpty(t, outside)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func BenchmarkStreamingCopy(b *testing.B) {
	for _, size := range []int64{1 << 20, 32 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(size)
			for b.Loop() {
				n, err := copyBody(discardWriter{}, io.LimitReader(zeroReader{}, size), 0)
				if err != nil || n != size {
					b.Fatalf("copy: %d, %v", n, err)
				}
			}
		})
	}
}
