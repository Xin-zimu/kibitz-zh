// Package relaycfg holds the relay's shipped connection limits in one place,
// so the binary and its acceptance tests cannot drift apart.
//
// # WHY THESE ARE NOT PARLEY'S DEFAULTS
//
// parley's relay defaults to 5 connection attempts per minute with a burst of
// 5, which is right for a relay that is reachable directly from the internet.
// It keys that limiter on r.RemoteAddr and — deliberately, since it is meant to
// be deployable without a proxy — never reads X-Forwarded-For or X-Real-IP.
//
// This deployment runs the relay behind nginx on one host, so every player
// arrives from the proxy's loopback address and all of them share a single
// bucket. The defaults therefore collapse into a site-wide budget: the sixth
// person to open the page gets HTTP 429. That was reproduced against the
// unmodified binary before this package existed; internal/clientip recovers the
// real client address and documents the same repro.
//
// # HOW THESE NUMBERS ARE USED
//
// internal/clientip rewrites RemoteAddr to the address nginx reports for the
// client before the relay's limiter sees it, so these limits apply per real
// client again. ConnBurst is what lets a household, an office or a school
// behind one NAT address — the deployment brief's "16 players from a single
// public IP" case — all open the page at once.
//
// Per-client throttling still belongs at the edge: nginx limit_req is the
// primary defence, and it must be kept in step with these numbers (see the
// deployment notes). The relay limit is the secondary defence, kept so that an
// attacker who reaches the service without passing through nginx is still
// bounded, and so the blind relay stays safe on its own if it is ever exposed
// directly.
//
// # SINGLE SOURCE OF TRUTH
//
// cmd/kibitz builds its relay.Options here, and the internal/clientip
// acceptance tests construct the real relay from the same Options() value, so
// lowering a shipped limit below what the brief requires fails the test suite
// instead of quietly shipping.
package relaycfg

import (
	"time"

	"github.com/richardwooding/parley/relay"
	"golang.org/x/time/rate"
)

const (
	// MaxSessions is the relay-wide cap on live sessions. The -max-sessions
	// flag defaults to it and overrides it when set.
	MaxSessions = 1000

	// ConnRatePer is the sustained per-client allowance: one new connection
	// per this interval, i.e. 30 connections/minute. Slow enough that a client
	// which lives off reconnect attempts is still throttled.
	ConnRatePer = 2 * time.Second

	// ConnBurst is the per-client token-bucket depth, i.e. how many connection
	// attempts may arrive back-to-back before the sustained rate takes over. It
	// must stay above the brief's 16-players-behind-one-NAT case; 20 leaves a
	// little headroom without turning the limiter into a formality.
	ConnBurst = 20
)

// Options returns the relay configuration the shipped binary runs with. Router
// is deliberately left nil: cmd/kibitz sets it only when Fly affinity routing
// is enabled, which is a property of where the process runs, not of the limits.
func Options() relay.Options {
	return relay.Options{
		MaxSessions: MaxSessions,
		ConnRate:    rate.Every(ConnRatePer),
		ConnBurst:   ConnBurst,
	}
}
