package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustParse(t *testing.T, spec string) Config {
	t.Helper()
	cfg, err := ParseTrusted(spec)
	if err != nil {
		t.Fatalf("ParseTrusted(%q): %v", spec, err)
	}
	return cfg
}

func req(remoteAddr string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// A request from the trusted local proxy is attributed to the client nginx
// reports, not to the proxy itself. This is the whole point of the package: it
// is what restores per-client rate-limit buckets behind the reverse proxy.
func TestTrustedProxyYieldsRealClient(t *testing.T) {
	cfg := mustParse(t, "127.0.0.0/8,::1/128")
	got := cfg.ClientAddr(req("127.0.0.1:54321", map[string]string{
		"X-Real-IP":       "203.0.113.7",
		"X-Forwarded-For": "203.0.113.7",
	}))
	if got != "203.0.113.7" {
		t.Fatalf("ClientAddr = %q, want 203.0.113.7", got)
	}
}

// SECURITY: a client that reaches the service DIRECTLY (not via the proxy) must
// not be able to choose its own bucket by sending a forged header. The socket
// peer wins, so spoofing cannot evade the limiter.
func TestForgedHeaderFromUntrustedPeerIsIgnored(t *testing.T) {
	cfg := mustParse(t, "127.0.0.0/8,::1/128")
	for _, peer := range []string{"198.51.100.9:40000", "203.0.113.50:1234"} {
		got := cfg.ClientAddr(req(peer, map[string]string{
			"X-Real-IP":       "10.1.2.3",
			"X-Forwarded-For": "10.1.2.3, 10.9.9.9",
		}))
		if got != peer {
			t.Fatalf("peer %s spoofed its address: ClientAddr = %q, want the socket peer", peer, got)
		}
	}
}

// A trusted proxy that forwards a malformed value must not be able to crash or
// silently fabricate an address; the socket peer (the proxy) is used instead.
func TestMalformedForwardedHeaderFallsBackToPeer(t *testing.T) {
	cfg := mustParse(t, "127.0.0.0/8")
	for _, bad := range []string{"not-an-ip", "999.1.1.1", "  ", "::gg", "1.2.3.4.5"} {
		got := cfg.ClientAddr(req("127.0.0.1:1111", map[string]string{"X-Real-IP": bad}))
		if got != "127.0.0.1:1111" {
			t.Fatalf("X-Real-IP=%q: ClientAddr = %q, want the peer unchanged", bad, got)
		}
	}
}

// X-Real-IP is produced by nginx as $remote_addr and so is fully proxy-
// controlled; it must take precedence over the client-appendable X-Forwarded-For
// chain. Otherwise a client could inject a left-most XFF entry to pick its bucket.
func TestRealIPBeatsForwardedFor(t *testing.T) {
	cfg := mustParse(t, "127.0.0.0/8")
	got := cfg.ClientAddr(req("127.0.0.1:2222", map[string]string{
		"X-Real-IP":       "203.0.113.7",
		"X-Forwarded-For": "1.2.3.4, 5.6.7.8",
	}))
	if got != "203.0.113.7" {
		t.Fatalf("ClientAddr = %q, want the proxy-asserted X-Real-IP", got)
	}
}

// With no forwarded headers at all (e.g. running without a proxy) behaviour is
// unchanged from upstream: the socket peer is used.
func TestNoHeadersKeepsPeer(t *testing.T) {
	cfg := mustParse(t, "127.0.0.0/8")
	if got := cfg.ClientAddr(req("192.0.2.5:9999", nil)); got != "192.0.2.5:9999" {
		t.Fatalf("ClientAddr = %q, want the peer unchanged", got)
	}
}

// The trust list is configurable; a non-loopback proxy hop can be trusted when
// the deployment needs it, and CIDR mistakes surface as errors rather than
// silently changing the trust boundary.
func TestConfigurableTrustList(t *testing.T) {
	cfg := mustParse(t, "10.0.0.0/8")
	if got := cfg.ClientAddr(req("10.1.2.3:5555", map[string]string{"X-Real-IP": "203.0.113.9"})); got != "203.0.113.9" {
		t.Fatalf("trusted 10/8 peer: ClientAddr = %q, want 203.0.113.9", got)
	}
	// Loopback is NOT trusted in this configuration.
	if got := cfg.ClientAddr(req("127.0.0.1:5555", map[string]string{"X-Real-IP": "203.0.113.9"})); got != "127.0.0.1:5555" {
		t.Fatalf("loopback should be untrusted here: ClientAddr = %q", got)
	}
	if _, err := ParseTrusted("10.0.0.0/8,bogus"); err == nil {
		t.Fatal("ParseTrusted should reject an invalid CIDR")
	}
}

// The middleware must actually rewrite RemoteAddr, because that is the only
// thing parley's limiter reads.
func TestHandlerRewritesRemoteAddr(t *testing.T) {
	cfg := mustParse(t, "127.0.0.0/8")
	var seen string
	h := cfg.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))
	h.ServeHTTP(httptest.NewRecorder(), req("127.0.0.1:3333", map[string]string{
		"X-Real-IP": "203.0.113.44",
	}))
	if seen != "203.0.113.44:0" {
		t.Fatalf("handler left RemoteAddr = %q, want rewritten to the client", seen)
	}
}

// And it must leave the request alone for an untrusted peer.
func TestHandlerLeavesUntrustedPeerAlone(t *testing.T) {
	cfg := mustParse(t, "127.0.0.0/8")
	var seen string
	h := cfg.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))
	h.ServeHTTP(httptest.NewRecorder(), req("198.51.100.1:3333", map[string]string{
		"X-Real-IP": "203.0.113.44",
	}))
	if seen != "198.51.100.1:3333" {
		t.Fatalf("handler rewrote an untrusted peer: %q", seen)
	}
}

// DefaultConfig must be loopback-only — a wrong default would let anyone on the
// internet choose their rate-limit bucket.
func TestDefaultConfigIsLoopbackOnly(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Trusted) == 0 {
		t.Fatal("DefaultConfig trusts nothing; forwarded headers would never work")
	}
	if got := cfg.ClientAddr(req("127.0.0.1:1", map[string]string{"X-Real-IP": "203.0.113.1"})); got != "203.0.113.1" {
		t.Fatalf("loopback should be trusted by default: got %q", got)
	}
	if got := cfg.ClientAddr(req("198.51.100.1:1", map[string]string{"X-Real-IP": "203.0.113.1"})); got != "198.51.100.1:1" {
		t.Fatalf("public peer must NOT be trusted by default: got %q", got)
	}
}
