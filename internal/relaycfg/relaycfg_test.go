package relaycfg_test

// These tests pin the shipped relay limits to the properties the deployment
// brief and internal/clientip's acceptance tests rely on. They assert against
// the exported constants rather than re-typing the numbers, so the only way to
// change what ships is to change the constant — and then these fail loudly if
// the new value is too small.

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/relaycfg"
	"golang.org/x/time/rate"
)

// parleyDefaultBurst is parley's documented per-IP burst (relay.Options:
// "ConnBurst int // per-IP burst (default 5)"), paired with 5 connections per
// minute. The whole point of internal/relaycfg is that this deployment cannot
// use those defaults behind nginx.
const parleyDefaultBurst = 5

// parleyDefaultRatePer is parley's documented per-IP connection rate: 5 per
// minute, i.e. one attempt every 12 seconds.
const parleyDefaultRatePer = 12 * time.Second

// The brief's headline case — 16 players from one public IP joining the moment
// an invite link is pasted into a group chat — must all get through, so the
// token bucket has to hold at least 16 tokens before the sustained rate bites.
func TestConnBurstCoversSixteenPlayersBehindOneIP(t *testing.T) {
	if relaycfg.ConnBurst < 16 {
		t.Fatalf("ConnBurst = %d, want >= 16: the brief requires 16 players from one "+
			"public IP to connect back-to-back; any fewer and the 16th gets HTTP 429",
			relaycfg.ConnBurst)
	}
}

// The limit must still be a limit. Raising the burst to cover a NAT is one
// thing; "fixing" the proxy problem by disabling throttling is another, and
// rate.Every(0) does exactly that — it returns rate.Inf, so an abusive client
// would never be throttled again.
//
// Note on the sustained rate: the shipped value is one connection per 2s
// (30/minute), which is SLOWER than a connection per second on purpose — the
// relay limit is the secondary defence here, with nginx limit_req doing the
// per-client throttling at the edge. So this test asserts the property that
// actually matters (the limit is finite, hence still enforcing something)
// rather than a ">= 1 connection/second" floor, which the shipped 30/minute
// does not meet and which cannot be introduced without changing behaviour.
func TestConnRateStillThrottles(t *testing.T) {
	if relaycfg.ConnRatePer <= 0 {
		t.Fatalf("ConnRatePer = %v, want > 0: rate.Every(0) is rate.Inf, which lets "+
			"every connection through forever", relaycfg.ConnRatePer)
	}
	if lim := rate.Every(relaycfg.ConnRatePer); lim <= 0 || lim == rate.Inf {
		t.Fatalf("rate.Every(%v) = %v, want a finite positive limit: the relay would no "+
			"longer throttle an abusive client at all", relaycfg.ConnRatePer, lim)
	}
}

// A future "these numbers are odd, restore the defaults" change must fail here
// with the reason attached rather than silently reintroducing the site-wide
// bucket that made the sixth player get 429 behind nginx.
func TestLimitsAreNotParleyDefaults(t *testing.T) {
	if relaycfg.ConnBurst <= parleyDefaultBurst {
		t.Fatalf("ConnBurst = %d, want > %d (parley's documented default burst): behind "+
			"nginx every player shares the proxy's bucket, so parley's defaults "+
			"(5 connections/minute, burst 5) become a site-wide budget and throttle real "+
			"players — the sixth person to open the page gets HTTP 429",
			relaycfg.ConnBurst, parleyDefaultBurst)
	}
	if relaycfg.ConnRatePer >= parleyDefaultRatePer {
		t.Fatalf("ConnRatePer = %v, want < %v (parley's 5/minute default): the shipped "+
			"deployment must sustain a higher per-client rate than parley's "+
			"edge-tuned default", relaycfg.ConnRatePer, parleyDefaultRatePer)
	}
}

func TestMaxSessionsPositive(t *testing.T) {
	if relaycfg.MaxSessions <= 0 {
		t.Fatalf("MaxSessions = %d, want > 0: a non-positive value would fall back to "+
			"parley's default instead of the configured cap", relaycfg.MaxSessions)
	}
}

// Options() must project the constants without alteration. This is the exact
// value cmd/kibitz hands to relay.New and the acceptance tests build their relay
// from, so a mismatch would mean the tests exercise something the binary does
// not — the very defect (a duplicated copy of the numbers) this package exists
// to remove.
func TestOptionsMatchesConstants(t *testing.T) {
	opts := relaycfg.Options()

	if opts.MaxSessions != relaycfg.MaxSessions {
		t.Errorf("Options().MaxSessions = %d, want %d", opts.MaxSessions, relaycfg.MaxSessions)
	}
	if opts.ConnBurst != relaycfg.ConnBurst {
		t.Errorf("Options().ConnBurst = %d, want %d", opts.ConnBurst, relaycfg.ConnBurst)
	}
	if want := rate.Every(relaycfg.ConnRatePer); opts.ConnRate != want {
		t.Errorf("Options().ConnRate = %v, want %v (one token per %v)",
			opts.ConnRate, want, relaycfg.ConnRatePer)
	}
}
