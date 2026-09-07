package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-w", "0"}, {"-w", "-1"}, {"-w", "101"}, {"-t", "0"}, {"-t", "9223372036854775807"},
		{"--max-size", "-1"}, {"-u", ""}, {"-u", "not-a-url"}, {"-u", "file:///tmp/file"},
		{"-u", "http://localhost:99999/file"}, {"-u", "http://localhost:/file"},
		{"-p", ""}, {"-p", "socks5://localhost:1080"}, {"-p", "http://localhost/proxy"},
		{"-H", "no-colon"}, {"-H", "bad name: value"}, {"-H", "X-Test: value\n"}, {"-H", "X-Test: a\r\nb"},
		{"unexpected"}, {"--unknown"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, _, err := parseConfig(args, io.Discard); err == nil {
				t.Fatal("expected argument error")
			}
		})
	}
}

func TestConfigCompatibility(t *testing.T) {
	cfg, done, err := parseConfig([]string{"-u", "https://example.com/a#fragment", "-H", "X-Test: first", "-H", "X-Test: second", "-H", "Host: example.org", "-t", "7", "-w", "3", "-p", "http://localhost:8080"}, io.Discard)
	if err != nil || done {
		t.Fatalf("parse: %v, done=%v", err, done)
	}
	if cfg.timeout != 7*time.Second || cfg.workers != 3 || cfg.headers.Get("X-Test") != "second" || cfg.proxy.Host != "localhost:8080" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	u, err := parseURL(cfg.url)
	if err != nil || u.Fragment != "" || u.Path != "/a" {
		t.Fatalf("fragment handling: %v %v", u, err)
	}
}
