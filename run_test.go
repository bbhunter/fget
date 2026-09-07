package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("input unavailable") }

func TestRunExitCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	for _, tc := range []struct {
		name    string
		args    []string
		input   io.Reader
		code    int
		message string
	}{
		{"help", []string{"--help"}, nil, 0, ""}, {"version", []string{"--version"}, nil, 0, ""},
		{"arguments", []string{"--workers", "0"}, nil, 2, "--workers"},
		{"empty", nil, strings.NewReader(" \n\r\n"), 2, "no URLs"},
		{"invalid URL", nil, strings.NewReader("invalid\n"), 2, "input line 1"},
		{"read error", nil, failingReader{}, 2, "input unavailable"},
		{"long line", nil, strings.NewReader(strings.Repeat("a", 1024*1024+1)), 2, "token too long"},
		{"HTTP error", []string{"-u", server.URL + "/missing"}, nil, 1, "HTTP 404"},
		{"partial success", nil, strings.NewReader(server.URL + "/ok\n" + server.URL + "/missing\n"), 1, "Downloaded: 1; failed: 1"},
		{"invalid and valid", nil, strings.NewReader("bad\n" + server.URL + "/ok\n"), 2, "Downloaded: 1; failed: 1"},
		{"success", nil, strings.NewReader("\r\n  " + server.URL + "/ok  \r\n"), 0, "Downloaded: 1; failed: 0; bytes: 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--no-folders", "-o", t.TempDir()}, tc.args...)
			var out, errOut bytes.Buffer
			code := run(context.Background(), args, tc.input, &out, &errOut)
			if code != tc.code || !strings.Contains(errOut.String(), tc.message) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if tc.name == "help" && !strings.Contains(out.String(), "--max-size") {
				t.Fatal("missing help")
			}
			if tc.name == "version" && !strings.Contains(out.String(), "fget ") {
				t.Fatal("missing version")
			}
		})
	}
}

func TestInputIsProcessedBeforeEOF(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requested <- struct{}{}; fmt.Fprint(w, "ok") }))
	defer server.Close()
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan int, 1)
	dir := t.TempDir()
	go func() { done <- run(ctx, []string{"-o", dir, "--no-folders"}, in, io.Discard, io.Discard) }()
	if _, err := fmt.Fprintln(writer, server.URL+"/file"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("download waited for EOF")
	}
	writer.Close()
	if code := <-done; code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestCancelWhileWaitingForInput(t *testing.T) {
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	dir := t.TempDir()
	go func() { done <- run(ctx, []string{"-o", dir}, in, io.Discard, io.Discard) }()
	// Writing a blank line synchronizes with the scanner, then it waits for more.
	if _, err := fmt.Fprintln(writer); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel blocked on stdin")
	}
}

func TestWorkerBound(t *testing.T) {
	var active, maxActive atomic.Int32
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, "ok")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var input strings.Builder
	for i := range 6 {
		fmt.Fprintf(&input, "%s/file%d\n", server.URL, i)
	}
	done := make(chan int, 1)
	dir := t.TempDir()
	go func() {
		done <- run(ctx, []string{"-w", "2", "-o", dir, "--no-folders"}, strings.NewReader(input.String()), io.Discard, io.Discard)
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workers did not start")
		}
	}
	close(release)
	if code := <-done; code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if maxActive.Load() != 2 {
		t.Fatalf("max active=%d", maxActive.Load())
	}
}

func TestErrorOutputHidesURLCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "failed", 500) }))
	defer server.Close()
	raw := strings.Replace(server.URL, "http://", "http://user:password@", 1) + "/file?token=secret"
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"-u", raw, "-o", t.TempDir()}, nil, io.Discard, &errOut); code != 1 {
		t.Fatalf("exit code %d", code)
	}
	for _, secret := range []string{"password", "secret", "token="} {
		if strings.Contains(errOut.String(), secret) {
			t.Fatalf("secret in diagnostics: %s", errOut.String())
		}
	}
}

func TestOutputDirectoryFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"-o", file, "--no-folders"}, strings.NewReader(""), io.Discard, &errOut); code != 1 {
		t.Fatalf("exit code %d: %s", code, errOut.String())
	}
}
