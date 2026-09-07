package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestExplicitProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "http://example.invalid/file" {
			t.Errorf("proxy received %s", r.URL)
		}
		fmt.Fprint(w, "proxied")
	}))
	defer proxy.Close()
	cfg := testConfig(t)
	cfg.proxy, _ = url.Parse(proxy.URL)
	if _, n, err := testDownload(t, context.Background(), cfg, "http://example.invalid/file"); err != nil || n != 7 {
		t.Fatalf("proxy: %d, %v", n, err)
	}
}

func TestEnvironmentProxyIsIgnored(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client := newClient(testConfig(t))
	defer client.CloseIdleConnections()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("environment proxy unexpectedly enabled")
	}
}

func TestAgentOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "my-agent" {
			t.Error("explicit agent did not take precedence")
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	cfg := testConfig(t)
	cfg.randomAgent = true
	cfg.headers.Set("User-Agent", "my-agent")
	if _, _, err := testDownload(t, context.Background(), cfg, server.URL+"/file"); err != nil {
		t.Fatal(err)
	}
}
