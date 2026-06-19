// Copyright 2026 conduyt. Licensed under Apache-2.0. See LICENSE.

package client

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ptaramona/conduyt-crm-cli/internal/config"
)

// buildSentAuthorization replays do()'s header construction (via the shared
// applyAuthorization helper, exactly as do() calls it) and returns the
// Authorization that net/http would transmit on the wire for the INITIAL request.
func buildSentAuthorization(authHeader string, cfg *config.Config, overrides map[string]string) string {
	h := http.Header{}
	applyAuthorization(h, authHeader, cfg, overrides)
	return h.Get("Authorization")
}

// TestApplyAuthorizationPrecedence proves applyAuthorization resolves the wire
// Authorization with last-write-wins precedence: cfg.AuthHeader() <
// cfg.Headers["Authorization"] < headerOverrides["Authorization"], and that an
// explicitly-present empty Authorization is an explicit empty Set (anon), not a
// silent fallback to a lower-precedence credential.
func TestApplyAuthorizationPrecedence(t *testing.T) {
	sent := func(headers map[string]string, cfg *config.Config) string {
		var authHeader string
		if cfg != nil {
			authHeader = cfg.AuthHeader()
		}
		return buildSentAuthorization(authHeader, cfg, headers)
	}

	if got := sent(nil, nil); got != "" {
		t.Fatalf("expected empty (anon) Authorization, got %q", got)
	}

	bearer := &config.Config{AuthHeaderVal: "Bearer bearer-cred"}
	if sent(nil, bearer) != "Bearer bearer-cred" {
		t.Fatal("cfg.AuthHeader() credential should be sent when nothing overrides it")
	}

	// Config.Headers["Authorization"] overrides cfg.AuthHeader().
	both := &config.Config{
		AuthHeaderVal: "Bearer bearer-cred",
		Headers:       map[string]string{"Authorization": "Bearer static-cred"},
	}
	if sent(nil, both) != "Bearer static-cred" {
		t.Fatalf("Config.Headers[Authorization] should win over AuthHeader(), got %q", sent(nil, both))
	}

	// Per-call headerOverrides["Authorization"] wins over everything.
	override := map[string]string{"Authorization": "Bearer override-cred"}
	if sent(override, both) != "Bearer override-cred" {
		t.Fatalf("headerOverrides[Authorization] must win, got %q", sent(override, both))
	}

	// Case-insensitive header key still counts (http.Header canonicalizes).
	lower := map[string]string{"authorization": "Bearer override-cred"}
	if sent(lower, both) != sent(override, both) {
		t.Fatal("authorization header lookup must be case-insensitive")
	}

	// An explicitly-present but EMPTY override collapses to anon, NOT a revert.
	emptyOverride := map[string]string{"Authorization": ""}
	if got := sent(emptyOverride, both); got != "" {
		t.Fatalf("explicit empty Authorization override should yield empty, got %q", got)
	}

	emptyStaticBoth := &config.Config{
		AuthHeaderVal: "Bearer bearer-cred",
		Headers:       map[string]string{"Authorization": ""},
	}
	if got := sent(nil, emptyStaticBoth); got != "" {
		t.Fatalf("explicit empty Config.Headers[Authorization] should yield empty, got %q", got)
	}
}

// TestApplyAuthorizationDeterministicForMixedCaseDuplicates proves that a config
// (or override) map containing BOTH "Authorization" and "authorization" with
// different values resolves to one DETERMINISTIC wire credential regardless of
// Go's nondeterministic map iteration order — so the byte the server receives is
// never a coin-flip.
func TestApplyAuthorizationDeterministicForMixedCaseDuplicates(t *testing.T) {
	cases := []struct {
		name      string
		authVal   string
		headers   map[string]string
		overrides map[string]string
	}{
		{
			name:    "config has both Authorization and authorization",
			headers: map[string]string{"Authorization": "Bearer upper-cred", "authorization": "Bearer lower-cred"},
		},
		{
			name:      "override has both Authorization and authorization",
			authVal:   "Bearer bearer-cred",
			overrides: map[string]string{"Authorization": "Bearer up-override", "authorization": "Bearer low-override"},
		},
		{
			name:    "three case variants in config",
			headers: map[string]string{"Authorization": "v1", "authorization": "v2", "AUTHORIZATION": "v3"},
		},
		{
			name:      "duplicate in config overridden by override",
			headers:   map[string]string{"Authorization": "Bearer a", "authorization": "Bearer b"},
			overrides: map[string]string{"authorization": "Bearer final"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{AuthHeaderVal: tc.authVal, Headers: tc.headers}
			var first string
			for i := 0; i < 50; i++ {
				sent := buildSentAuthorization(cfg.AuthHeader(), cfg, tc.overrides)
				if i == 0 {
					first = sent
					continue
				}
				if sent != first {
					t.Fatalf("nondeterministic resolution: iteration %d gave %q, want %q", i, sent, first)
				}
			}
			if first == "" && (len(tc.headers) > 0 || len(tc.overrides) > 0) {
				t.Fatalf("expected a non-empty Authorization to be sent, got empty")
			}
		})
	}
}

// TestDoStripsURLUserinfoFromWire is the wire-level proof that do() clears
// req.URL.User so net/http can no longer inject an untracked Basic header from
// the BaseURL's userinfo, and that an explicit Authorization is what reaches the
// server.
func TestDoStripsURLUserinfoFromWire(t *testing.T) {
	var sentAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sentAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// BaseURL carries userinfo AND an explicit bearer is set. Explicit must win,
	// and the URL userinfo's Basic header must NOT leak onto the wire.
	srvURL, _ := url.Parse(srv.URL)
	srvURL.User = url.UserPassword("sneaky", "creds")

	c := &Client{
		BaseURL:    srvURL.String(),
		Config:     &config.Config{AuthHeaderVal: "Bearer explicit-token"},
		HTTPClient: &http.Client{CheckRedirect: secureRedirect},
		NoCache:    true,
	}

	if _, err := c.Get("/ping", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if sentAuth != "Bearer explicit-token" {
		t.Fatalf("wire Authorization = %q, want explicit bearer (URL userinfo leaked or overrode it)", sentAuth)
	}

	// With NO explicit auth, a BaseURL userinfo must NOT inject any Basic header,
	// because do() strips req.URL.User and applyAuthorization is the sole producer
	// of the wire Authorization (which sees no credential here).
	sentAuth = "unset"
	c2 := &Client{
		BaseURL:    srvURL.String(),
		HTTPClient: &http.Client{CheckRedirect: secureRedirect},
		NoCache:    true,
	}
	if _, err := c2.Get("/ping", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if sentAuth != "" {
		t.Fatalf("wire Authorization = %q, want empty (no untracked Basic from URL userinfo)", sentAuth)
	}
}

// TestRedirectDoesNotInjectUserinfoBasicAuth is the round-5 HIGH regression:
// net/http follows redirects INSIDE Do(), and a Location URL carrying userinfo
// would make it inject a Basic Authorization header derived from that userinfo on
// the redirected request — a credential we never computed. secureRedirect clears
// req.URL.User on every hop, so the final request must carry NO Authorization
// header when the only would-be source is the redirect's userinfo.
func TestRedirectDoesNotInjectUserinfoBasicAuth(t *testing.T) {
	var finalAuth string
	finalAuth = "unset"

	// Final destination records whatever Authorization it receives.
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer dest.Close()

	destURL, _ := url.Parse(dest.URL)

	// Origin server redirects to the SAME host but with userinfo in the Location,
	// which net/http would otherwise convert into a Basic header on the next hop.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := *destURL
		loc.User = url.UserPassword("attacker", "creds")
		loc.Path = "/final"
		http.Redirect(w, r, loc.String(), http.StatusFound)
	}))
	defer origin.Close()

	// Point dest's handler at /final too (same mux, single handler) — it already
	// records on any path. Make origin and dest the SAME host by pointing the
	// client at origin and letting it redirect to dest (different port = different
	// host:port, so this also exercises the cross-host Authorization strip; here
	// there is no Authorization to begin with, so the key assertion is no Basic
	// gets injected from userinfo).

	c := &Client{
		BaseURL:    origin.URL,
		HTTPClient: newHTTPClient(0, nil),
		NoCache:    true,
	}
	if _, err := c.Get("/start", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if finalAuth != "" {
		t.Fatalf("redirect injected an untracked Authorization %q; userinfo in Location must NOT become a Basic header", finalAuth)
	}
}

// TestRedirectStripsAuthorizationAcrossHostChange proves secureRedirect removes
// the caller's Authorization header when a redirect crosses to a DIFFERENT host,
// so a bearer/Basic credential is never leaked to an unrelated origin.
func TestRedirectStripsAuthorizationAcrossHostChange(t *testing.T) {
	var otherHostAuth string
	otherHostAuth = "unset"

	// "Other host" destination records the Authorization it receives.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHostAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer other.Close()

	// Origin (a different host:port) redirects to `other`.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
	}))
	defer origin.Close()

	c := &Client{
		BaseURL:    origin.URL,
		Config:     &config.Config{AuthHeaderVal: "Bearer secret-token"},
		HTTPClient: newHTTPClient(0, nil),
		NoCache:    true,
	}
	if _, err := c.Get("/start", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if otherHostAuth != "" {
		t.Fatalf("Authorization %q leaked to a different host across a redirect; it must be stripped", otherHostAuth)
	}
}

// TestRedirectPreservesAuthorizationSameHost confirms the cross-host strip does
// NOT over-trigger: a same-host redirect still forwards the caller's
// Authorization (the legitimate behavior, e.g. trailing-slash normalization).
func TestRedirectPreservesAuthorizationSameHost(t *testing.T) {
	var landedAuth string
	landedAuth = "unset"

	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/landed", http.StatusFound)
	})
	mux.HandleFunc("/landed", func(w http.ResponseWriter, r *http.Request) {
		landedAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &Client{
		BaseURL:    srv.URL,
		Config:     &config.Config{AuthHeaderVal: "Bearer keep-me"},
		HTTPClient: newHTTPClient(0, nil),
		NoCache:    true,
	}
	if _, err := c.Get("/start", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if landedAuth != "Bearer keep-me" {
		t.Fatalf("same-host redirect dropped Authorization: got %q, want the original bearer", landedAuth)
	}
}

// TestRedirectStripsAuthorizationAcrossMultiHopSameSubdomain is the Finding-2
// HIGH regression: origin -> subdomain -> SAME-subdomain. net/http copies the
// original request's headers onto EVERY hop, so on hop 2 (subdomain -> same
// subdomain) the original Authorization is re-attached. A previous-hop check
// (host unchanged from hop 1) would wave it through, re-leaking the token to the
// foreign origin reached on hop 1. Anchoring the comparison to via[0] (the
// initial origin) keeps Authorization stripped for the whole chain once we leave
// the initial host.
func TestRedirectStripsAuthorizationAcrossMultiHopSameSubdomain(t *testing.T) {
	var hop2Auth string
	hop2Auth = "unset"

	// "Subdomain" host. /b1 redirects to /b2 on the SAME host; /b2 records auth.
	subMux := http.NewServeMux()
	var subURL string
	subSrv := httptest.NewServer(subMux)
	defer subSrv.Close()
	subURL = subSrv.URL
	subMux.HandleFunc("/b1", func(w http.ResponseWriter, r *http.Request) {
		// hop 2: redirect to the SAME (sub) host.
		http.Redirect(w, r, subURL+"/b2", http.StatusFound)
	})
	subMux.HandleFunc("/b2", func(w http.ResponseWriter, r *http.Request) {
		hop2Auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	})

	// Origin (the initial host). hop 1: redirect off-origin to the sub host.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, subURL+"/b1", http.StatusFound)
	}))
	defer origin.Close()

	c := &Client{
		BaseURL:    origin.URL,
		Config:     &config.Config{AuthHeaderVal: "Bearer secret-token"},
		HTTPClient: newHTTPClient(0, nil),
		NoCache:    true,
	}
	if _, err := c.Get("/start", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if hop2Auth != "" {
		t.Fatalf("Authorization %q re-leaked on the 2nd redirect hop (same-subdomain); once off the initial origin it must stay stripped", hop2Auth)
	}
}

// TestNormalizeOriginScoping proves CacheScope distinguishes origins/keys and is
// stable, so the auto-cache filename cannot collide across tenants. It also
// confirms default-port normalization (https vs :443) maps to one scope.
func TestNormalizeOriginScoping(t *testing.T) {
	scope := func(baseURL, auth string) string {
		c := &Client{BaseURL: baseURL, Config: &config.Config{AuthHeaderVal: auth}}
		return c.CacheScope()
	}

	a := scope("https://conduyt.app/api/v1", "Bearer key-A")
	b := scope("https://conduyt.app/api/v1", "Bearer key-B")
	if a == b {
		t.Fatal("different keys on the same origin must yield different cache scopes")
	}

	origin2 := scope("https://other.example.com/api/v1", "Bearer key-A")
	if origin2 == a {
		t.Fatal("different origins with the same key must yield different cache scopes")
	}

	// Same logical origin, default port spelled out: must collapse to one scope.
	if scope("https://conduyt.app/api/v1", "Bearer key-A") != scope("https://conduyt.app:443/api/v1", "Bearer key-A") {
		t.Fatal("default https port (:443) should normalize to the same scope")
	}

	// Different API base path on the same host = different tenant = different scope.
	if scope("https://conduyt.app/api/v1", "Bearer key-A") == scope("https://conduyt.app/t/acme/api/v1", "Bearer key-A") {
		t.Fatal("different base paths on the same host must yield different cache scopes")
	}

	// Stability.
	if scope("https://conduyt.app/api/v1", "Bearer key-A") != a {
		t.Fatal("CacheScope must be stable for identical inputs")
	}

	// The scope never embeds the raw token.
	if got := scope("https://conduyt.app/api/v1", "Bearer super-secret-token"); strings.Contains(got, "super-secret-token") {
		t.Fatalf("cache scope must not embed the raw token, got %q", got)
	}
}

// TestRedirectCapStopsRunawayChain confirms secureRedirect preserves net/http's
// 10-redirect safety cap.
func TestRedirectCapStopsRunawayChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer srv.Close()

	c := &Client{
		BaseURL:    srv.URL,
		HTTPClient: newHTTPClient(0, nil),
		NoCache:    true,
	}
	if _, err := c.Get("/loop", nil); err == nil {
		t.Fatal("expected an error from an infinite redirect loop, got nil")
	}
}
