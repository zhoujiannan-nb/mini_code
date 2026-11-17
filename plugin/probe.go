package plugin

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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
	_, _ = resp.Body.Read(make([]byte, 512))
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

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
