//go:build ignore

// Run from the repository root: go run ./scripts/release.go -version v1.0.0
package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func main() {
	version := flag.String("version", "dev", "dev or a v-prefixed semantic version")
	out := flag.String("output", "dist", "artifact directory")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := build(ctx, *version, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(ctx context.Context, version, output string) error {
	if !regexp.MustCompile(`^(dev|v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?)$`).MatchString(version) {
		return fmt.Errorf("invalid release version %q", version)
	}
	out, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(out, ".build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var checksums strings.Builder
	for _, target := range []struct{ os, arch string }{
		{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"},
		{"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"},
	} {
		binary := "fget"
		if target.os == "windows" {
			binary += ".exe"
		}
		binaryPath := filepath.Join(stage, binary)
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X main.version="+version, "-o", binaryPath, ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s/%s: %w", target.os, target.arch, err)
		}
		name := fmt.Sprintf("fget_%s_%s_%s.zip", version, target.os, target.arch)
		archive := filepath.Join(out, name)
		if err := archiveFiles(archive, binaryPath, binary); err != nil {
			return err
		}
		file, err := os.Open(archive)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(&checksums, "%x  %s\n", hash.Sum(nil), name)
		fmt.Println(archive)
	}
	return os.WriteFile(filepath.Join(out, "checksums.txt"), []byte(checksums.String()), 0644)
}

func archiveFiles(destination, binaryPath, binary string) (err error) {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer func() {
		if e := file.Close(); err == nil {
			err = e
		}
	}()
	archive := zip.NewWriter(file)
	defer func() {
		if e := archive.Close(); err == nil {
			err = e
		}
	}()
	files := []struct {
		path, name string
		mode       os.FileMode
	}{
		{binaryPath, binary, 0755}, {"README.md", "README.md", 0644},
	}
	if _, err := os.Stat("LICENSE"); err == nil {
		files = append(files, struct {
			path, name string
			mode       os.FileMode
		}{"LICENSE", "LICENSE", 0644})
	}
	for _, entry := range files {
		if err := addFile(archive, entry.path, entry.name, entry.mode); err != nil {
			return err
		}
	}
	return nil
}

func addFile(archive *zip.Writer, source, name string, mode os.FileMode) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
	w, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, file)
	return err
}
