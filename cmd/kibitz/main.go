// Command kibitz is the relay server. It serves the embedded web client at /
// and (from M1) the WebSocket relay at /ws. The relay only ever forwards
// opaque encrypted frames between session participants — it can never read
// service traffic (see docs/THREAT-MODEL.md).
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/richardwooding/flyaffinity"
	"github.com/richardwooding/kibitz/internal/clientip"
	"github.com/richardwooding/kibitz/internal/dashboard"
	"github.com/richardwooding/kibitz/internal/pushfwd"
	"github.com/richardwooding/kibitz/internal/relaycfg"
	"github.com/richardwooding/kibitz/web"
	"github.com/richardwooding/parley/relay"
	"github.com/richardwooding/parley/wire"
)

// clusterToken derives the internal peer-stats auth token from the dashboard
// cookie key, so every machine computes the same value without a new secret.
func clusterToken(cookieKey []byte) []byte {
	m := hmac.New(sha256.New, cookieKey)
	_, _ = m.Write([]byte("parley-cluster-stats"))
	return m.Sum(nil)
}

// dashboardConfigFromEnv reads the admin dashboard's config from environment
// (Fly secrets). It returns ok=false when any required value is missing, so the
// dashboard stays disabled unless fully configured — the repo and local builds
// carry no secrets and expose nothing.
func dashboardConfigFromEnv() (dashboard.Config, bool) {
	id := os.Getenv("DASHBOARD_GITHUB_CLIENT_ID")
	secret := os.Getenv("DASHBOARD_GITHUB_CLIENT_SECRET")
	key := os.Getenv("DASHBOARD_COOKIE_KEY")
	base := os.Getenv("DASHBOARD_BASE_URL")
	var users []string
	for u := range strings.SplitSeq(os.Getenv("DASHBOARD_ALLOW"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			users = append(users, u)
		}
	}
	if id == "" || secret == "" || len(key) < 16 || base == "" || len(users) == 0 {
		return dashboard.Config{}, false
	}
	return dashboard.Config{
		ClientID: id, ClientSecret: secret, CookieKey: []byte(key),
		Allow: users, BaseURL: base, AppName: "kibitz",
	}, true
}

// precompressed serves an embedded asset's brotli (.br) or gzip (.gz) sibling
// when the client accepts it, else falls back to the raw FileServer. This keeps
// the relay a dumb static server while shipping the smallest bytes; the wasm
// core and the whole JS/CSS/HTML shell are precompressed at build time.
func precompressed(dist fs.FS, raw http.Handler) http.HandlerFunc {
	encs := []struct{ token, ext string }{{"br", ".br"}, {"gzip", ".gz"}}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			raw.ServeHTTP(w, r)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		ae := r.Header.Get("Accept-Encoding")
		for _, e := range encs {
			if !strings.Contains(ae, e.token) {
				continue
			}
			b, err := fs.ReadFile(dist, p+e.ext)
			if err != nil {
				continue
			}
			// Revalidate every load against a content ETag: an unchanged asset
			// costs a cheap 304, but a deploy is picked up immediately. The
			// embedded files carry no modtime, so without this the browser can
			// heuristically cache them and serve a stale shell after a deploy.
			etag := fmt.Sprintf("\"%x\"", sha256.Sum256(b))
			h := w.Header()
			h.Set("ETag", etag)
			h.Set("Cache-Control", "no-cache")
			h.Add("Vary", "Accept-Encoding")
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			h.Set("Content-Encoding", e.token)
			h.Set("Content-Type", contentType(p))
			_, _ = w.Write(b)
			return
		}
		raw.ServeHTTP(w, r)
	}
}

// contentType maps a served path to its media type (the precompressed sibling
// hides the real extension from net/http's sniffer, so we set it explicitly).
func contentType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".wasm":
		return "application/wasm"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json", ".webmanifest":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// version is stamped by goreleaser via -ldflags "-X main.version=...".
var version = "dev"

// displayVersion is what the UI shows: "dev" as-is, otherwise "vX.Y.Z".
func displayVersion() string {
	if version == "dev" || version == "" {
		return "dev"
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	// Default comes from internal/relaycfg, the same source the relay is built
	// from, so the flag cannot silently shadow the shipped cap.
	maxSessions := flag.Int("max-sessions", relaycfg.MaxSessions, "maximum concurrent sessions")
	showVersion := flag.Bool("version", false, "print version and exit")
	// Comma-separated CIDRs whose X-Real-IP / X-Forwarded-For headers are
	// believed. Loopback only by default, which is all a same-host nginx needs.
	trustedProxiesFlag := flag.String("trusted-proxies", "127.0.0.0/8,::1/128",
		"comma-separated CIDRs allowed to assert a client IP via forwarded headers")
	flag.Parse()

	if *showVersion {
		fmt.Println("kibitz", version)
		os.Exit(0)
	}
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatalf("embedded web client: %v", err)
	}

	mux := http.NewServeMux()
	files := http.FileServerFS(dist)
	// Serve the smallest precompressed encoding each client accepts (brotli, then
	// gzip), falling back to the raw file. `make wasm` precompresses every
	// servable asset — the ~9MB wasm core AND the JS/CSS/HTML shell — so a first
	// mobile load ships ~2MB (brotli) instead of ~2.6MB.
	mux.Handle("/", precompressed(dist, files))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
	// The web client fetches this to show its version badge. Return the
	// display form: "dev" for local builds, else "vX.Y.Z".
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, displayVersion())
	})
	// Session-affinity routing: when running on Fly with >1 machine, pin every
	// connection for a session to one machine (its in-memory relay is
	// authoritative for that shard). Off Fly / single machine → serve-here,
	// identical to today.
	var aff *flyaffinity.Resolver
	var router func(wire.SessionID, *http.Request) relay.RouteResult
	if mid := os.Getenv("FLY_MACHINE_ID"); mid != "" {
		aff = flyaffinity.New(os.Getenv("FLY_APP_NAME"), mid, 7*time.Second)
		router = aff.Route
		log.Printf("fly affinity routing enabled (machine %s, app %s)", mid, os.Getenv("FLY_APP_NAME"))
	}
	relayOpts := relaycfg.Options()
	// The -max-sessions flag overrides the shipped cap; 0 keeps the configured
	// default rather than falling through to parley's (identical) fallback.
	if *maxSessions > 0 {
		relayOpts.MaxSessions = *maxSessions
	}
	relayOpts.Router = router
	// Per-client connection budget. parley's defaults (5/minute, burst 5) are
	// tuned for the relay running directly on an edge network; behind the nginx
	// reverse proxy used by this deployment every player shares the proxy's
	// address, so the default turns into a site-wide budget and the sixth person
	// to open the page gets 429. Why these numbers, and why they are not
	// parley's defaults, lives in internal/relaycfg — the single source of truth
	// shared with the acceptance tests, so the tested limits are the shipped
	// ones. See internal/clientip for how the real client address is recovered,
	// and the deployment notes for the matching nginx limit_req zone.
	relaySrv := relay.New(relayOpts)
	defer relaySrv.Close()
	// Rewrite RemoteAddr to the address nginx reports, but only for requests
	// that actually arrived from a trusted proxy hop. Without this the relay's
	// per-IP limiter buckets every player under 127.0.0.1 even with the limits
	// above raised.
	trustedProxies, err := clientip.ParseTrusted(*trustedProxiesFlag)
	if err != nil {
		log.Fatalf("trusted proxies: %v", err)
	}
	if len(trustedProxies.Trusted) == 0 {
		trustedProxies = clientip.DefaultConfig()
	}
	mux.Handle("/ws", trustedProxies.Handler(relaySrv))
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "%s %s\n", os.Getenv("FLY_MACHINE_ID"), os.Getenv("FLY_REGION"))
	})
	// Keyless Web Push forwarder: clients sign an empty VAPID push in-browser
	// (CORS forbids posting to push services directly) and this forwards it. It
	// holds no keys and sees no game content — see internal/pushfwd.
	mux.Handle("/push", pushfwd.New())

	// Admin dashboard (GitHub-OAuth-gated) — only wired up when its Fly secrets
	// are present, so it's dormant locally and in the public build.
	if cfg, ok := dashboardConfigFromEnv(); ok {
		src := dashboard.StatsSource(relaySrv)
		if aff != nil {
			token := clusterToken(cfg.CookieKey)
			mux.Handle(dashboard.InternalStatsPath(), dashboard.InternalStatsHandler(relaySrv, token))
			src = dashboard.NewAggregator(relaySrv, aff.Peers, token)
		}
		dashboard.New(cfg, src).Register(mux)
		log.Printf("admin dashboard enabled at /dashboard (%d allowed user(s))", len(cfg.Allow))
	} else {
		log.Print("admin dashboard disabled (set DASHBOARD_* env vars to enable)")
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout/ReadTimeout: /ws connections are long-lived; the
		// relay enforces its own per-frame idle deadline.
	}
	log.Printf("kibitz %s listening on %s", version, *listen)
	log.Fatal(srv.ListenAndServe())
}
