package plugin

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// probeHTTP reports whether url answers with a 2xx within a short timeout. It is
// a liveness probe, not a health contract: a plugin that returns 404 from "/" is
// up, and that is enough to say "running".
func probeHTTP(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused rather than left half-open.
	_, _ = resp.Body.Read(make([]byte, 512))
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// probeTCP reports whether something accepts a TCP connection on the host:port
// named by raw. raw may be "host:port", "tcp://host:port" or an http(s) URL, so
// the same value works for both probe types.
func probeTCP(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return false
		}
		host := u.Host
		if u.Port() == "" {
			if u.Scheme == "https" {
				host = net.JoinHostPort(u.Hostname(), "443")
			} else {
				host = net.JoinHostPort(u.Hostname(), "80")
			}
		}
		raw = host
	}
	conn, err := net.DialTimeout("tcp", raw, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
