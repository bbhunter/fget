package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

func download(ctx context.Context, client *http.Client, root *os.Root, cfg config, u *url.URL) (saved string, size int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	name := outputName(u, cfg.unique)
	dir := root
	relative := name
	if !cfg.noFolders {
		host := safeComponent(u.Host)
		if err := root.MkdirAll(host, 0755); err != nil {
			return "", 0, fmt.Errorf("create host directory: %w", err)
		}
		var err error
		dir, err = root.OpenRoot(host)
		if err != nil {
			return "", 0, fmt.Errorf("open host directory: %w", err)
		}
		defer dir.Close()
		relative = filepath.Join(host, name)
	}
	if !cfg.overwrite {
		if _, err := dir.Lstat(name); err == nil {
			return "", 0, errors.New("destination already exists; use --unique or --overwrite")
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", 0, fmt.Errorf("check destination: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, fmt.Errorf("create request: %w", err)
	}
	req.Header = cfg.headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", userAgent(cfg.randomAgent))
	}
	if host := req.Header.Get("Host"); host != "" {
		req.Host = host
		req.Header.Del("Host")
	}
	resp, err := client.Do(req)
	if err != nil {
		// url.Error includes the raw URL, which may contain credentials or tokens.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return "", 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if cfg.maxSize > 0 && resp.ContentLength > cfg.maxSize {
		return "", 0, fmt.Errorf("response exceeds --max-size (%d bytes)", cfg.maxSize)
	}
	file, temp, err := createTemp(dir)
	if err != nil {
		return "", 0, fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		file.Close()
		if cleanupErr := dir.Remove(temp); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary file: %w", cleanupErr))
		}
	}()
	size, err = copyBody(file, resp.Body, cfg.maxSize)
	if err != nil {
		return "", 0, fmt.Errorf("download body: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return "", 0, fmt.Errorf("close response: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("close file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if err := publishFile(dir, temp, name, cfg.overwrite); err != nil {
		return "", 0, err
	}
	return filepath.Join(cfg.outputRoot(), relative), size, nil
}
