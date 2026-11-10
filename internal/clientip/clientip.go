// Package clientip derives the real client address for rate limiting when the
// service runs behind a reverse proxy.
//
// # WHY THIS EXISTS
//
// parley's relay throttles new connections per client IP, but it keys that
// bucket on r.RemoteAddr and — deliberately, since the relay is meant to be
// deployable directly — never consults X-Forwarded-For or X-Real-IP. Upstream
// kibitz runs on Fly's edge where RemoteAddr already is the real client, so this
// was never visible there.
//
// Behind nginx every player arrives from 127.0.0.1, so the relay's default
// budget (5 connections/minute, burst 5) collapses into a single site-wide
// budget: the sixth person to open the page gets HTTP 429. That was reproduced
// against the unmodified binary before this package existed.
//
// The fix has two halves, and BOTH are required:
//
//  1. This middleware rewrites RemoteAddr to the address nginx reports for the
//     client, so the relay's existing per-IP limiter buckets correctly again.
//  2. The relay's configured limits are raised to a level appropriate for a
//     private friends-and-family deployment (see internal/relaycfg).
//
// # TRUST MODEL — the security-critical part
//
// A forwarded header is only believed when the request actually came from a
// trusted proxy hop (loopback by default, plus any configured CIDR). An attacker
// who reaches the service directly cannot spoof a header to escape their bucket,
// because their connection's RemoteAddr is not a trusted proxy and the header is
// ignored.
//
// The relay also binds to 127.0.0.1 in the shipped systemd unit, so the only way
// to reach it is through nginx in the first place; this middleware is the
// defence-in-depth for the case where that binding is ever relaxed.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Config describes which proxy hops may assert a client address.
type Config struct {
	// Trusted lists CIDRs whose forwarded headers are believed. The default
	// (see DefaultConfig) is loopback only, which is all a same-host nginx
	// deployment needs.
	Trusted []*net.IPNet
}

// DefaultConfig trusts loopback only: nginx on the same machine connects from
// 127.0.0.1 (or ::1), and nothing else should be able to influence the result.
func DefaultConfig() Config {
	var trusted []*net.IPNet
	for _, cidr := range []string{"127.0.0.0/8", "::1/128"} {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		trusted = append(trusted, n)
	}
	return Config{Trusted: trusted}
}

// ParseTrusted parses a comma-separated CIDR list (e.g. from a flag or env
// var). Entries that are not valid CIDRs are reported so a typo cannot silently
// widen or narrow the trust boundary.
func ParseTrusted(spec string) (Config, error) {
	cfg := Config{}
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			return Config{}, fmt.Errorf("clientip: invalid trusted CIDR %q: %w", raw, err)
		}
		cfg.Trusted = append(cfg.Trusted, n)
	}
	return cfg, nil
}

// isTrusted reports whether addr (host or host:port) belongs to a trusted hop.
func (c Config) isTrusted(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range c.Trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientAddr returns the address to attribute the request to.
//
// It returns the forwarded address only when the request came from a trusted
// proxy AND that header parses as an IP; otherwise it returns the socket peer
// unchanged. Returning the peer on any doubt is deliberate: a spoofed-but-
// ignored header must never be able to move a client into a fresh bucket.
func (c Config) ClientAddr(r *http.Request) string {
	peer := r.RemoteAddr
	if !c.isTrusted(peer) {
		return peer
	}
	// X-Real-IP is set by nginx to $remote_addr, i.e. the actual peer of the
	// proxy — exactly what we want. Prefer it over X-Forwarded-For, which is a
	// client-appendable chain.
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// The left-most entry is the originating client. We only reach here when
		// the hop is trusted, and nginx is configured to overwrite (not append
		// to) this header, so the value is proxy-controlled.
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip.String()
		}
	}
	return peer
}

// Handler rewrites r.RemoteAddr to the derived client address before delegating.
//
// The rewrite is what makes parley's per-IP limiter work behind a proxy: the
// relay reads r.RemoteAddr and nothing else, so adjusting it here changes the
// limiter key without touching (or forking) parley.
func (c Config) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if addr := c.ClientAddr(r); addr != r.RemoteAddr {
			// Keep a host:port shape so downstream net.SplitHostPort still works.
			if ip := net.ParseIP(addr); ip != nil {
				r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			}
		}
		next.ServeHTTP(w, r)
	})
}
