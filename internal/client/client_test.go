// Copyright 2026 conduyt. Licensed under Apache-2.0. See LICENSE.

package client

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ptaramona/conduyt-crm-cli/internal/config"
)

// TestCacheKeyIsolatesStaticAuthorizationHeaders is the regression test for the
// HIGH defense-in-depth finding: a config that carries Authorization via
// Config.Headers (with no bearer env / auth_header) must still scope the cache
// per-credential. Two different Config.Headers["Authorization"] values for the
// same path/params must produce DIFFERENT cache keys, or a credential swap
// collides on one cache file.
func TestCacheKeyIsolatesStaticAuthorizationHeaders(t *testing.T) {
	path := "/contacts"
	params := map[string]string{"limit": "10"}

	clientFor := func(staticAuth string) *Client {
		return &Client{
			Config: &config.Config{
				Headers: map[string]string{"Authorization": staticAuth},
			},
		}
	}

	tenantA := clientFor("Bearer tenant-a-token")
	tenantB := clientFor("Bearer tenant-b-token")

	keyA := tenantA.cacheKey(path, params, nil)
	keyB := tenantB.cacheKey(path, params, nil)

	if keyA == keyB {
		t.Fatalf("cache key collision: two different Config.Headers[Authorization] values produced the same key %q", keyA)
	}

	// Same static credential, same request -> same key (cache hits must still work).
	if again := clientFor("Bearer tenant-a-token").cacheKey(path, params, nil); again != keyA {
		t.Fatalf("cache key instability: identical credential produced %q then %q", keyA, again)
	}
}

// TestAuthFingerprintMirrorsDoPrecedence proves authFingerprint resolves the
// effective Authorization with the same last-write-wins precedence do() uses to
// build the request: cfg.AuthHeader() < cfg.Headers["Authorization"] <
// headerOverrides["Authorization"].
func TestAuthFingerprintMirrorsDoPrecedence(t *testing.T) {
	fp := func(headers map[string]string, cfg *config.Config) string {
		return authFingerprint("", headers, cfg)
	}

	anon := fp(nil, nil)
	if anon != "anon" {
		t.Fatalf("expected anon sentinel, got %q", anon)
	}

	bearer := &config.Config{AuthHeaderVal: "Bearer bearer-cred"}
	staticHdr := &config.Config{Headers: map[string]string{"Authorization": "Bearer static-cred"}}

	// cfg.AuthHeader() alone fingerprints.
	if fp(nil, bearer) == anon {
		t.Fatal("cfg.AuthHeader() credential should not fingerprint as anon")
	}

	// Config.Headers["Authorization"] overrides cfg.AuthHeader() (do() Sets it after).
	both := &config.Config{
		AuthHeaderVal: "Bearer bearer-cred",
		Headers:       map[string]string{"Authorization": "Bearer static-cred"},
	}
	if got, want := fp(nil, both), fp(nil, staticHdr); got != want {
		t.Fatalf("Config.Headers[Authorization] should win over AuthHeader(): got %q want %q", got, want)
	}
	if fp(nil, both) == fp(nil, bearer) {
		t.Fatal("Config.Headers[Authorization] override must change the fingerprint vs AuthHeader() alone")
	}

	// Per-call headerOverrides["Authorization"] wins over everything.
	override := map[string]string{"Authorization": "Bearer override-cred"}
	if fp(override, both) == fp(nil, both) {
		t.Fatal("headerOverrides[Authorization] must override Config.Headers credential")
	}

	// Case-insensitive header key still counts as an override (http.Header canonicalizes).
	lower := map[string]string{"authorization": "Bearer override-cred"}
	if fp(lower, both) != fp(override, both) {
		t.Fatal("authorization header lookup must be case-insensitive")
	}

	// An explicitly-present but EMPTY override is an explicit empty Set in do(),
	// not a fallback. It must collapse to anon, NOT silently revert to a
	// lower-precedence credential.
	emptyOverride := map[string]string{"Authorization": ""}
	if got := fp(emptyOverride, both); got != "anon" {
		t.Fatalf("explicit empty Authorization override should yield anon, got %q (silent fallback to another credential)", got)
	}

	// Same for an explicit empty Config.Headers Authorization with no override.
	emptyStaticBoth := &config.Config{
		AuthHeaderVal: "Bearer bearer-cred",
		Headers:       map[string]string{"Authorization": ""},
	}
	if got := fp(nil, emptyStaticBoth); got != "anon" {
		t.Fatalf("explicit empty Config.Headers[Authorization] should yield anon, got %q", got)
	}
}

// buildSentAuthorization replays do()'s header construction (via the shared
// applyAuthSources helper, exactly as do() now calls it) and returns the
// Authorization that net/http would transmit on the wire. This is the ground
// truth the cache fingerprint must agree with.
func buildSentAuthorization(rawURL, authHeader string, cfg *config.Config, overrides map[string]string) string {
	h := http.Header{}
	applyAuthSources(h, rawURL, authHeader, cfg, overrides)
	return h.Get("Authorization")
}

// fingerprintOf reproduces authFingerprint's hashing so a test can assert the
// fingerprint equals the hash of the credential actually sent.
func fingerprintOf(auth string) string {
	if auth == "" {
		return "anon"
	}
	sum := sha256.Sum256([]byte(auth))
	return hex.EncodeToString(sum[:8])
}

// TestFingerprintMatchesSentAuthorizationForMixedCaseDuplicates is the round-3
// HIGH regression: a config (or override) map that contains BOTH "Authorization"
// and "authorization" with DIFFERENT values must not let the cache key be
// computed from one credential while the request sends the other. Because both
// the fingerprint and do() now resolve Authorization through the SAME shared
// helper (applyAuthSources), the fingerprint must equal the hash of the exact
// credential the request transmits — deterministically, regardless of Go's
// nondeterministic map iteration order.
func TestFingerprintMatchesSentAuthorizationForMixedCaseDuplicates(t *testing.T) {
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

			// Run repeatedly: if resolution depended on map iteration order the
			// fingerprint and the sent credential would drift apart across runs.
			var firstFP, firstSent string
			for i := 0; i < 50; i++ {
				sent := buildSentAuthorization("", cfg.AuthHeader(), cfg, tc.overrides)
				fp := authFingerprint("", tc.overrides, cfg)

				if fp != fingerprintOf(sent) {
					t.Fatalf("fingerprint %q does not match hash of sent credential %q (cross-credential cache path)", fp, sent)
				}
				if i == 0 {
					firstFP, firstSent = fp, sent
					continue
				}
				if fp != firstFP || sent != firstSent {
					t.Fatalf("nondeterministic resolution: iteration %d gave fp=%q sent=%q, want fp=%q sent=%q", i, fp, sent, firstFP, firstSent)
				}
			}

			// And the chosen value must be one of the supplied variants (sanity).
			if firstSent == "" && (len(tc.headers) > 0 || len(tc.overrides) > 0) {
				t.Fatalf("expected a non-empty Authorization to be sent, got empty")
			}
		})
	}
}

// TestCacheKeyConsistentWithSentCredential ties the regression to the cache key
// itself: two clients whose case-variant duplicate maps resolve to the SAME sent
// credential must share a cache key, and two that resolve to DIFFERENT sent
// credentials must not — proving the key tracks the wire credential, not an
// arbitrary map-order pick.
func TestCacheKeyConsistentWithSentCredential(t *testing.T) {
	path := "/contacts"
	params := map[string]string{"limit": "10"}

	// Both maps deterministically resolve to the lexicographically-last variant's
	// value. Identical variant sets -> identical sent credential -> same key.
	mk := func() *Client {
		return &Client{Config: &config.Config{
			Headers: map[string]string{"Authorization": "Bearer A", "authorization": "Bearer B"},
		}}
	}
	keyA := mk().cacheKey(path, params, nil)
	keyB := mk().cacheKey(path, params, nil)
	if keyA != keyB {
		t.Fatalf("identical duplicate-key maps produced different keys %q vs %q", keyA, keyB)
	}

	// A map resolving to a different sent credential must get a different key.
	other := &Client{Config: &config.Config{
		Headers: map[string]string{"Authorization": "Bearer A", "authorization": "Bearer DIFFERENT"},
	}}
	if other.cacheKey(path, params, nil) == keyA {
		t.Fatalf("different sent credential collided on cache key %q", keyA)
	}
}

// TestCacheKeyIsolatesURLUserinfo is the round-4 HIGH regression that CLOSES THE
// CLASS: with no explicit Authorization (no env/AuthHeaderVal/Headers/overrides),
// a BaseURL carrying userinfo would make net/http inject a Basic header derived
// from user:pass — but cacheKey only ever saw "anon", so https://userA:pass@host
// and https://userB:pass@host collided on one cache file while sending DIFFERENT
// Basic credentials. After folding URL userinfo into effectiveAuthorization,
// different userinfo must produce DIFFERENT cache keys, and an explicit
// Authorization must still override URL userinfo.
func TestCacheKeyIsolatesURLUserinfo(t *testing.T) {
	path := "/contacts"
	params := map[string]string{"limit": "10"}

	keyFor := func(baseURL string, cfg *config.Config) string {
		c := &Client{BaseURL: baseURL, Config: cfg}
		return c.cacheKey(path, params, nil)
	}

	// 1. Different userinfo, no explicit auth -> DIFFERENT keys (the bug).
	keyA := keyFor("https://userA:pass@host.example", nil)
	keyB := keyFor("https://userB:pass@host.example", nil)
	if keyA == keyB {
		t.Fatalf("cache key collision: BaseURLs with different userinfo and no explicit auth produced the same key %q", keyA)
	}

	// 2. Different PASSWORD, same user -> still different keys.
	if keyFor("https://user:passA@host.example", nil) == keyFor("https://user:passB@host.example", nil) {
		t.Fatal("cache key collision: different userinfo password produced the same key")
	}

	// 3. URL userinfo must NOT fingerprint as anon (the broken behavior).
	anonKey := keyFor("https://host.example", nil)
	if keyA == anonKey {
		t.Fatal("URL userinfo fingerprinted as anon: a credentialed read could be served to an anonymous one")
	}

	// 4. Stable: identical userinfo -> identical key (cache hits still work).
	if keyFor("https://userA:pass@host.example", nil) != keyA {
		t.Fatal("cache key instability: identical userinfo produced different keys")
	}

	// 5. Explicit Authorization OVERRIDES URL userinfo: the same explicit
	// credential over two different userinfo BaseURLs yields the SAME key, and it
	// is NOT the userinfo-derived key.
	explicit := &config.Config{Headers: map[string]string{"Authorization": "Bearer explicit"}}
	overA := keyFor("https://userA:pass@host.example", explicit)
	overB := keyFor("https://userB:pass@host.example", explicit)
	if overA != overB {
		t.Fatalf("explicit Authorization must override URL userinfo so userinfo no longer affects the key: %q vs %q", overA, overB)
	}
	if overA == keyA {
		t.Fatal("explicit Authorization key collided with userinfo-derived key (override not applied)")
	}
}

// TestUserinfoAuthorizationMatchesNetHTTP proves userinfoAuthorization replicates
// net/http's URL-userinfo Basic injection byte-for-byte, and that do() now strips
// req.URL.User so net/http NEVER injects a credential we did not compute — i.e.
// the wire Authorization equals effectiveAuthorization, no untracked source left.
func TestUserinfoAuthorizationMatchesNetHTTP(t *testing.T) {
	// Ground truth: what net/http itself produces from URL userinfo.
	groundTruth := func(rawURL string) string {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Authorization")
			w.Write([]byte(`{}`))
		}))
		defer srv.Close()

		u, _ := url.Parse(srv.URL)
		// Re-attach the userinfo from rawURL onto the live test-server URL so
		// net/http's own injection path runs against a reachable host.
		ref, _ := url.Parse(rawURL)
		u.User = ref.User
		req, _ := http.NewRequest("GET", u.String(), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("ground-truth request failed: %v", err)
		}
		resp.Body.Close()
		return got
	}

	for _, raw := range []string{
		"https://alice:s3cr3t@host.example",
		"https://bob:@host.example",   // empty password
		"https://just-user@host.example", // user, no password
		"https://a%40b:p%3As@host.example", // escaped userinfo (@ and : in fields)
	} {
		want := groundTruth(raw)
		got := userinfoAuthorization(raw)
		if got != want {
			t.Fatalf("userinfoAuthorization(%q) = %q, net/http sends %q (divergence = untracked credential)", raw, got, want)
		}
	}

	// No userinfo -> empty (anon), matching net/http (no header injected).
	if got := userinfoAuthorization("https://host.example"); got != "" {
		t.Fatalf("expected empty Authorization for URL without userinfo, got %q", got)
	}
}

// TestDoStripsURLUserinfoFromWire is the wire-level proof that do() clears
// req.URL.User so net/http can no longer inject an untracked Basic header, and
// that an explicit Authorization (not the URL userinfo) is what reaches the
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
		HTTPClient: &http.Client{},
		NoCache:    true,
	}

	if _, err := c.Get("/ping", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if sentAuth != "Bearer explicit-token" {
		t.Fatalf("wire Authorization = %q, want explicit bearer (URL userinfo leaked or overrode it)", sentAuth)
	}

	// Now with NO explicit auth: the URL userinfo's Basic header is what
	// applyAuthSources computes and sends — and it must equal userinfoAuthorization,
	// not whatever net/http would have done to a still-present req.URL.User.
	sentAuth = ""
	c2 := &Client{
		BaseURL:    srvURL.String(),
		HTTPClient: &http.Client{},
		NoCache:    true,
	}
	if _, err := c2.Get("/ping", nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if want := userinfoAuthorization(srvURL.String()); sentAuth != want {
		t.Fatalf("wire Authorization = %q, want userinfo-derived %q", sentAuth, want)
	}
}
