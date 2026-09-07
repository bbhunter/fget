package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(t *testing.T) config {
	t.Helper()
	cfg, _, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.output, cfg.noFolders = t.TempDir(), true
	return cfg
}

func testDownload(t *testing.T, ctx context.Context, cfg config, raw string) (string, int64, error) {
	t.Helper()
	if err := os.MkdirAll(cfg.outputRoot(), 0755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(cfg.outputRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	u, err := parseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := newClient(cfg)
	defer client.CloseIdleConnections()
	return download(ctx, client, root, cfg, u)
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unexpected output: %v", entries)
	}
}

func TestDownloadAndExistingFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "value" || r.Host != "custom.example" || r.Header.Get("User-Agent") != "custom-agent" {
			t.Errorf("request headers: %v, host=%s", r.Header, r.Host)
		}
		fmt.Fprint(w, "new content")
	}))
	defer server.Close()
	cfg := testConfig(t)
	cfg.headers = http.Header{"X-Test": {"value"}, "Host": {"custom.example"}, "User-Agent": {"custom-agent"}}
	destination := filepath.Join(cfg.output, "file.txt")
	if err := os.WriteFile(destination, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testDownload(t, context.Background(), cfg, server.URL+"/file.txt"); err == nil {
		t.Fatal("existing file was accepted")
	}
	if data, _ := os.ReadFile(destination); string(data) != "original" {
		t.Fatal("existing file changed")
	}
	cfg.overwrite = true
	name, n, err := testDownload(t, context.Background(), cfg, server.URL+"/file.txt")
	if err != nil || n != 11 || name != destination {
		t.Fatalf("download: %s %d %v", name, n, err)
	}
	if data, _ := os.ReadFile(name); string(data) != "new content" {
		t.Fatalf("content: %q", data)
	}
	entries, _ := os.ReadDir(cfg.output)
	if len(entries) != 1 {
		t.Fatalf("temporary file left: %v", entries)
	}
}

func TestHTTPFailuresAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		max       int64
		chunked   bool
		wantError bool
	}{
		{"404", 404, "not found", 0, false, true}, {"500", 500, "error", 0, false, true},
		{"redirect", 302, "redirect body", 0, false, true}, {"known length", 200, "12345", 4, false, true},
		{"chunked", 200, "12345", 4, true, true}, {"exact limit", 200, "1234", 4, true, false},
		{"empty", 204, "", 4, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			cfg := testConfig(t)
			cfg.maxSize = tc.max
			_, _, err := testDownload(t, context.Background(), cfg, server.URL+"/file")
			if (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
			if tc.wantError {
				assertEmpty(t, cfg.output)
			}
		})
	}
}

func TestTruncatedResponsePreservesDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "short")
	}))
	defer server.Close()
	cfg := testConfig(t)
	cfg.overwrite = true
	file := filepath.Join(cfg.output, "file")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testDownload(t, context.Background(), cfg, server.URL+"/file"); err == nil {
		t.Fatal("expected truncated response error")
	}
	if data, _ := os.ReadFile(file); string(data) != "original" {
		t.Fatalf("original lost: %q", data)
	}
	entries, _ := os.ReadDir(cfg.output)
	if len(entries) != 1 {
		t.Fatal("temporary file left")
	}
}

func TestTLSVerification(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	cfg := testConfig(t)
	if _, _, err := testDownload(t, context.Background(), cfg, server.URL+"/file"); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	assertEmpty(t, cfg.output)
	cfg.insecure = true
	if _, _, err := testDownload(t, context.Background(), cfg, server.URL+"/file"); err != nil {
		t.Fatal(err)
	}
}

func TestRedirects(t *testing.T) {
	var sawOriginHeader, sawTarget atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTarget.Store(true)
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials forwarded to another origin")
		}
		fmt.Fprint(w, "done")
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/same", 302)
			return
		}
		sawOriginHeader.Store(r.Header.Get("X-API-Key") == "secret")
		http.Redirect(w, r, target.URL+"/end", 302)
	}))
	defer origin.Close()
	cfg := testConfig(t)
	cfg.followRedirect = true
	cfg.headers = http.Header{"X-Api-Key": {"secret"}, "Authorization": {"Bearer secret"}, "Cookie": {"session=secret"}}
	file, _, err := testDownload(t, context.Background(), cfg, origin.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(file) != "start" || !sawOriginHeader.Load() || !sawTarget.Load() {
		t.Fatal("redirect behavior mismatch")
	}
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", 302) }))
	defer loop.Close()
	if _, _, err := testDownload(t, context.Background(), cfg, loop.URL+"/loop"); err == nil {
		t.Fatal("redirect loop accepted")
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	for _, cancelDownload := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDownload), func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "partial")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			cfg := testConfig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelDownload {
				go func() { <-started; cancel() }()
			} else {
				cfg.timeout = 100 * time.Millisecond
			}
			_, _, err := testDownload(t, ctx, cfg, server.URL+"/file")
			if err == nil {
				t.Fatal("expected cancellation/timeout")
			}
			if cancelDownload && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cause: %v", err)
			}
			assertEmpty(t, cfg.output)
		})
	}
}

func TestConcurrentCollision(t *testing.T) {
	const workers = 8
	var arrived sync.WaitGroup
	arrived.Add(workers)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived.Done()
		arrived.Wait()
		fmt.Fprint(w, strings.Repeat("complete", 1024))
	}))
	defer server.Close()
	cfg := testConfig(t)
	root, err := os.OpenRoot(cfg.output)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	client := newClient(cfg)
	defer client.CloseIdleConnections()
	u, _ := url.Parse(server.URL + "/same")
	results := make(chan error, workers)
	for range workers {
		go func() { _, _, err := download(context.Background(), client, root, cfg, u); results <- err }()
	}
	successes := 0
	for range workers {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful publishers: %d", successes)
	}
	data, err := os.ReadFile(filepath.Join(cfg.output, "same"))
	if err != nil || !bytes.Equal(data, []byte(strings.Repeat("complete", 1024))) {
		t.Fatal("incomplete final file")
	}
	entries, _ := os.ReadDir(cfg.output)
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestDirectoryWriteError(t *testing.T) {
	cfg := testConfig(t)
	cfg.noFolders = false
	if err := os.MkdirAll(cfg.outputRoot(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.outputRoot(), "example.com"), []byte("file blocks directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testDownload(t, context.Background(), cfg, "https://example.com/file"); err == nil {
		t.Fatal("expected directory creation failure")
	}
}
