package clientip

// End-to-end rate-limit acceptance tests.
//
// These run the REAL parley relay behind the REAL client-IP middleware and drive
// real WebSocket upgrades, because the defect this fixes only appears at the
// handshake: before the fix, connection #6 from the proxy's address got HTTP 429
// and the player never entered the lobby.
//
// Acceptance criteria from the deployment brief:
//   - 6 players from one public IP joining quickly must all get through.
//   - 16 players from one public IP joining quickly must all get through.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/richardwooding/kibitz/internal/relaycfg"
	"github.com/richardwooding/parley/relay"
	"golang.org/x/time/rate"
)

// dialThroughOnce performs one WebSocket upgrade through the given handler and
// reports whether it was accepted.
func dialThroughOnce(t *testing.T, srv *httptest.Server, headers map[string]string, handler http.Handler) bool {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	hdr := http.Header{}
	for k, v := range headers {
		hdr.Set(k, v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			return false
		}
		// Any other failure is a test bug, not a rate-limit outcome.
		t.Fatalf("dial: %v (status %v)", err, statusOf(resp))
	}
	_ = conn.CloseNow()
	return true
}

func statusOf(resp *http.Response) any {
	if resp == nil {
		return "none"
	}
	return resp.StatusCode
}

// newProxiedRelay starts the relay behind the client-IP middleware, exactly as
// the deployment wires it — including the connection limits, which come from
// internal/relaycfg: the SAME Options() value cmd/kibitz hands to relay.New.
// This is what makes the assertions bite: lowering a shipped limit now fails
// these tests, instead of passing against a stale copy of the numbers.
func newProxiedRelay(t *testing.T, trusted string) *httptest.Server {
	t.Helper()
	cfg, err := ParseTrusted(trusted)
	if err != nil {
		t.Fatal(err)
	}
	r := relay.New(relaycfg.Options())
	t.Cleanup(r.Close)
	srv := httptest.NewServer(cfg.Handler(r))
	t.Cleanup(srv.Close)
	return srv
}

// THE REGRESSION THIS FIXES: many players behind one proxy must each get their
// own bucket. With 16 distinct X-Real-IP values, all 16 upgrades succeed even
// though they all arrive from the proxy's loopback address.
func TestSixteenDistinctClientsBehindProxyAllConnect(t *testing.T) {
	srv := newProxiedRelay(t, "127.0.0.0/8")

	for i := 1; i <= 16; i++ {
		ip := "203.0.113." + itoa(i)
		if !dialThroughOnce(t, srv, map[string]string{"X-Real-IP": ip}, nil) {
			t.Fatalf("client %d (%s) was rate limited; per-client buckets are not working", i, ip)
		}
	}
}

// Sixteen DISTINCT players, one shared public IP (a household / office / school
// NAT), joining as fast as they can — the brief's "16 people from the same
// public IP" case. They share one bucket, so the burst must cover them.
func TestSixteenSharedPublicIPAllConnect(t *testing.T) {
	srv := newProxiedRelay(t, "127.0.0.0/8")
	shared := map[string]string{"X-Real-IP": "198.51.100.42"}

	for i := 1; i <= 16; i++ {
		if !dialThroughOnce(t, srv, shared, nil) {
			t.Fatalf("shared-IP connection %d was rate limited (ConnBurst too low)", i)
		}
	}
}

// Six is the number that failed against the unmodified binary; keep it as an
// explicit acceptance case because it is the cheapest regression signal.
func TestSixSharedPublicIPAllConnect(t *testing.T) {
	srv := newProxiedRelay(t, "127.0.0.0/8")
	shared := map[string]string{"X-Real-IP": "198.51.100.77"}
	for i := 1; i <= 6; i++ {
		if !dialThroughOnce(t, srv, shared, nil) {
			t.Fatalf("connection %d from a shared IP was rate limited", i)
		}
	}
}

// Concurrent joins, which is what actually happens when an invite link is
// pasted into a group chat.
func TestSixteenConcurrentJoins(t *testing.T) {
	srv := newProxiedRelay(t, "127.0.0.0/8")

	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for i := 1; i <= 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ip := "203.0.113." + itoa(n)
			url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			hdr := http.Header{}
			hdr.Set("X-Real-IP", ip)
			conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
			if err != nil {
				if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
					errs <- ip + " got 429"
					return
				}
				errs <- ip + " dial error: " + err.Error()
				return
			}
			_ = conn.CloseNow()
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent join failed: %s", e)
	}
}

// The limiter must still be a limiter: a single abusive client exceeding the
// burst is throttled. This guards against "fixing" the proxy problem by simply
// disabling rate limiting.
func TestSingleClientStillRateLimited(t *testing.T) {
	cfg, err := ParseTrusted("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	// A deliberately tight limiter proves the mechanism is still wired up.
	tight := relay.New(relay.Options{ConnRate: rate.Every(time.Hour), ConnBurst: 2})
	t.Cleanup(tight.Close)
	srv := httptest.NewServer(cfg.Handler(tight))
	t.Cleanup(srv.Close)

	one := map[string]string{"X-Real-IP": "198.51.100.200"}
	if !dialThroughOnce(t, srv, one, nil) {
		t.Fatal("first connection should be allowed")
	}
	if !dialThroughOnce(t, srv, one, nil) {
		t.Fatal("second connection (within burst) should be allowed")
	}
	if dialThroughOnce(t, srv, one, nil) {
		t.Fatal("third connection should have been rate limited")
	}
}

// SECURITY: an attacker who bypasses nginx and reaches the service directly
// cannot escape their own bucket by forging a header.
//
// The test client connects over loopback, so to model "a peer that is NOT a
// trusted proxy" the trust list is configured to exclude loopback entirely. Every
// forged header is then ignored, all attempts land in the attacker's single
// bucket, and the tight limiter stops them after the burst.
func TestForgedHeaderCannotEscapeLimiter(t *testing.T) {
	// Trust only a CIDR the test client cannot match, so its forwarded headers
	// are treated as attacker-controlled.
	cfg, err := ParseTrusted("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	tight := relay.New(relay.Options{ConnRate: rate.Every(time.Hour), ConnBurst: 2})
	t.Cleanup(tight.Close)
	srv := httptest.NewServer(cfg.Handler(tight))
	t.Cleanup(srv.Close)

	allowed := 0
	for i := range 8 {
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		hdr := http.Header{}
		hdr.Set("X-Real-IP", "10.0.0."+itoa(i)) // rotating forgery
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
		cancel()
		if err == nil {
			allowed++
			_ = conn.CloseNow()
		}
	}
	// Burst is 2, so at most 2 (plus at most one token refill) may pass; the
	// eight rotating forgeries must NOT each mint a fresh bucket.
	if allowed > 3 {
		t.Fatalf("forged headers produced %d accepted connections (burst=2); "+
			"an untrusted peer escaped the rate limiter", allowed)
	}
	if allowed == 0 {
		t.Fatal("no connection was accepted; the limiter is misconfigured, not merely strict")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
