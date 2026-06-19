// Copyright 2026 conduyt. Licensed under Apache-2.0. See LICENSE.

package client

import (
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
		return authFingerprint(headers, cfg)
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
