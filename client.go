package main

import (
	"crypto/tls"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"time"
)

func newClient(cfg config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Retain explicit-proxy behavior; environment proxies are not used.
	tr.Proxy = nil
	if cfg.proxy != nil {
		tr.Proxy = http.ProxyURL(cfg.proxy)
	}
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.insecure} // Explicit opt-in only.
	tr.DialContext = (&net.Dialer{Timeout: cfg.timeout, KeepAlive: 30 * time.Second}).DialContext
	tr.MaxIdleConns = cfg.workers
	tr.MaxIdleConnsPerHost = cfg.workers
	tr.MaxConnsPerHost = cfg.workers
	tr.ResponseHeaderTimeout = cfg.timeout
	return &http.Client{
		Transport: tr,
		Timeout:   cfg.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !cfg.followRedirect {
				return http.ErrUseLastResponse
			}
			if len(via) > 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("unsupported redirect scheme")
			}
			// Do not propagate custom credentials to another origin or a downgrade.
			if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
				for name := range req.Header {
					if name != "User-Agent" {
						req.Header.Del(name)
					}
				}
				req.Host = ""
			}
			return nil
		},
	}
}

// Retained for --random-agent compatibility; prefer the identifying default
// User-Agent or an explicit -H 'User-Agent: ...'.
var legacyAgents = [...]string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:66.0) Gecko/20100101 Firefox/66.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_14_4) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/12.1 Safari/605.1.15",
}

func userAgent(random bool) string {
	if random {
		return legacyAgents[rand.IntN(len(legacyAgents))]
	}
	return "fget/" + buildVersion()
}
