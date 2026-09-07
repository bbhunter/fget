package main

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"unicode"
)

func safeComponent(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, " .")
	if s == "" {
		s = "index.html"
	}
	// Keep individual components and --unique names below common filesystem limits.
	var b strings.Builder
	for _, r := range s {
		if b.Len()+len(string(r)) > 100 {
			break
		}
		b.WriteRune(r)
	}
	s = strings.TrimRight(b.String(), " .")
	base, _, _ := strings.Cut(strings.ToUpper(s), ".")
	base = strings.TrimRight(base, " ")
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		(strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
			(len([]rune(base)) == 4 && strings.ContainsRune("123456789¹²³", []rune(base)[3])) {
		s = "_" + s
	}
	return s
}

func outputName(u *url.URL, unique bool) string {
	_, name := path.Split(u.Path)
	name = safeComponent(name)
	if unique {
		canonical := *u
		canonical.Fragment = ""
		digest := sha256.Sum256([]byte(canonical.String()))
		name = fmt.Sprintf("%s_%x_%s", safeComponent(u.Host), digest[:8], name)
	}
	return name
}

// A hard link publishes the completed temporary file without replacing a name
// created by another worker/process. Unsupported filesystems fail safely.
func publishFile(dir *os.Root, temp, name string, overwrite bool) error {
	if overwrite {
		return dir.Rename(temp, name)
	}
	if err := dir.Link(temp, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination already exists (use --unique or --overwrite): %w", err)
		}
		return fmt.Errorf("publish file (filesystem must support hard links): %w", err)
	}
	return nil
}

func createTemp(dir *os.Root) (*os.File, string, error) {
	for range 10 {
		name := ".fget-" + rand.Text() + ".part"
		file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", errors.New("cannot allocate a temporary filename")
}

func copyBody(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if limit == 0 {
		return io.Copy(dst, src)
	}
	n, err := io.Copy(dst, io.LimitReader(src, limit))
	if err != nil {
		return n, err
	}
	// Check one extra byte without overflowing limit when it is MaxInt64.
	extra, err := io.CopyN(io.Discard, src, 1)
	if extra != 0 {
		return n, fmt.Errorf("response exceeds --max-size (%d bytes)", limit)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	return n, nil
}
