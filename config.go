package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

type config struct {
	url, output                                                                  string
	workers                                                                      int
	timeout                                                                      time.Duration
	headers                                                                      http.Header
	proxy                                                                        *url.URL
	verbose, followRedirect, randomAgent, noFolders, unique, insecure, overwrite bool
	maxSize                                                                      int64
}

func parseConfig(args []string, out io.Writer) (config, bool, error) {
	cfg := config{headers: make(http.Header)}
	fs := pflag.NewFlagSet("fget", pflag.ContinueOnError)
	fs.SetOutput(out)
	var headers []string
	var proxy string
	var seconds int64
	var help, showVersion bool
	fs.StringVarP(&cfg.url, "url", "u", "", "Download one URL instead of reading stdin")
	fs.StringVarP(&cfg.output, "output", "o", "", "Base directory (adds results/host unless --no-folders)")
	fs.IntVarP(&cfg.workers, "workers", "w", 20, "Concurrent downloads (1-100)")
	fs.Int64VarP(&seconds, "timeout", "t", 20, "Total timeout per download, in seconds")
	fs.StringArrayVarP(&headers, "header", "H", nil, "Request header, repeatable: 'Name: value'")
	fs.StringVarP(&proxy, "proxy", "p", "", "HTTP or HTTPS proxy URL")
	fs.BoolVarP(&cfg.verbose, "verbose", "v", false, "Report each completed download")
	fs.BoolVarP(&cfg.followRedirect, "follow-redirect", "f", false, "Follow up to 10 redirects")
	fs.BoolVarP(&cfg.randomAgent, "random-agent", "r", false, "Choose a User-Agent from the legacy compatibility list")
	fs.BoolVar(&cfg.noFolders, "no-folders", false, "Save directly in the base directory (default: current directory)")
	fs.BoolVar(&cfg.unique, "unique", false, "Include the host and a stable URL hash in filenames")
	fs.BoolVar(&cfg.insecure, "insecure", false, "Disable TLS certificate verification")
	fs.BoolVar(&cfg.overwrite, "overwrite", false, "Replace existing files after a successful download")
	fs.Int64Var(&cfg.maxSize, "max-size", 0, "Maximum bytes per file (0 means unlimited)")
	fs.BoolVarP(&help, "help", "h", false, "Show help")
	fs.BoolVar(&showVersion, "version", false, "Show version")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: fget [flags]\n\nRead one HTTP(S) URL per line from stdin, or use --url.\n\nOptions:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cfg, false, err
	}
	if help {
		fs.Usage()
		return cfg, true, nil
	}
	if showVersion {
		fmt.Fprintf(out, "fget %s\n", buildVersion())
		return cfg, true, nil
	}
	if fs.NArg() != 0 {
		return cfg, false, errors.New("unexpected positional arguments; use --url or stdin")
	}
	if cfg.workers < 1 || cfg.workers > 100 {
		return cfg, false, errors.New("--workers must be between 1 and 100")
	}
	if seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
		return cfg, false, errors.New("--timeout must be a positive number of seconds within time.Duration range")
	}
	cfg.timeout = time.Duration(seconds) * time.Second
	if cfg.maxSize < 0 {
		return cfg, false, errors.New("--max-size cannot be negative")
	}
	if fs.Changed("url") {
		cfg.url = strings.TrimSpace(cfg.url)
		if _, err := parseURL(cfg.url); err != nil {
			return cfg, false, fmt.Errorf("--url: %w", err)
		}
	}
	if fs.Changed("proxy") {
		u, err := parseURL(proxy)
		if err != nil {
			return cfg, false, fmt.Errorf("--proxy: %w", err)
		}
		if u.Fragment != "" || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
			return cfg, false, errors.New("--proxy must contain only a host and optional port/credentials")
		}
		cfg.proxy = u
	}
	for _, raw := range headers {
		name, value, ok := strings.Cut(raw, ":")
		if !validHeaderValue(value) {
			return cfg, false, errors.New("--header contains an invalid control character")
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !validHeaderName(name) || !validHeaderValue(value) {
			return cfg, false, errors.New("--header must contain a valid 'Name: value'")
		}
		// Preserve the previous last-value-wins behavior.
		cfg.headers.Set(name, value)
	}
	return cfg, false, nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("expected an absolute HTTP(S) URL with a host")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("URL port must be between 1 and 65535")
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("URL contains an empty port")
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func validHeaderValue(s string) bool {
	for _, c := range s {
		if (c < 32 && c != '\t') || c == 127 {
			return false
		}
	}
	return true
}

func (cfg config) outputRoot() string {
	if cfg.noFolders {
		if cfg.output == "" {
			return "."
		}
		return cfg.output
	}
	return filepath.Join(cfg.output, "results")
}
