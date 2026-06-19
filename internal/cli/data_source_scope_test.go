// Copyright 2026 conduyt. Licensed under Apache-2.0. See LICENSE.
// PATCH: hand-authored regression tests for the 2026-06-18 dogfood security
// fixes (Finding 1: scoped auto-cache + --no-cache live-only). Not generated.

package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ptaramona/conduyt-crm-cli/internal/client"
	"github.com/ptaramona/conduyt-crm-cli/internal/config"
)

// newTestClient builds a Client pointed at baseURL with a given bearer key.
func newTestClient(baseURL, key string) *client.Client {
	return client.New(&config.Config{BaseURL: baseURL, AuthHeaderVal: "Bearer " + key}, 0, 0)
}

// TestAutoCache_NoCrossTenantLeakOnFallback is the Finding-1 HIGH regression:
// an auto-mode read under origin/key A write-throughs into a SCOPED cache. A
// later auto read under a DIFFERENT origin/key B whose API is unreachable must
// NOT fall back to A's cached rows — the scoped store namespaces by origin+auth,
// so B sees "no cached data", never A's data.
func TestAutoCache_NoCrossTenantLeakOnFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	// Server A: serves tenant-A rows. Stays up so the write-through populates
	// A's scoped cache.
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"tenant-a-secret"}]`))
	}))
	defer srvA.Close()

	cA := newTestClient(srvA.URL, "key-A")
	flags := &rootFlags{dataSource: "auto"}
	dataA, _, err := resolveRead(ctx, cA, flags, "contacts", true, "/contacts", nil, nil)
	if err != nil {
		t.Fatalf("tenant-A live read failed: %v", err)
	}
	if string(dataA) == "" {
		t.Fatal("tenant-A read returned empty data")
	}

	// Server B is created then immediately closed → connection refused, which
	// isNetworkError() classifies as a network error (the fallback path).
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srvBURL := srvB.URL
	srvB.Close()

	cB := newTestClient(srvBURL, "key-B")
	_, _, errB := resolveRead(ctx, cB, flags, "contacts", true, "/contacts", nil, nil)
	if errB == nil {
		t.Fatal("tenant-B read should have errored (API down, no cache for B's scope) — instead it returned data, indicating a cross-tenant leak")
	}

	// Same origin/key A, API now down: A's own scoped cache SHOULD serve the
	// fallback — proving the scoping isolates without breaking same-tenant reuse.
	srvADownURL := srvA.URL
	srvA.Close() // close A so a fresh client to it gets connection-refused
	cADown := newTestClient(srvADownURL, "key-A")
	dataA2, prov, errA2 := resolveRead(ctx, cADown, flags, "contacts", true, "/contacts", nil, nil)
	if errA2 != nil {
		t.Fatalf("tenant-A fallback to its OWN scoped cache failed: %v", errA2)
	}
	if prov.Source != "local" {
		t.Fatalf("expected local provenance on fallback, got %q", prov.Source)
	}
	if string(dataA2) == "" {
		t.Fatal("tenant-A fallback returned empty data from its own cache")
	}
}

// TestAutoCache_NoCacheForcesLiveOnly is the Finding-1 --no-cache regression:
// with --no-cache, the auto path must never write-through and never fall back to
// a persisted store. When the API is unreachable the call errors out rather than
// silently serving any cached (possibly cross-tenant) data.
func TestAutoCache_NoCacheForcesLiveOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	// Prime a scoped cache for key-X via a normal auto read.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"x-row"}]`))
	}))
	url := srv.URL

	c := newTestClient(url, "key-X")
	flags := &rootFlags{dataSource: "auto"}
	if _, _, err := resolveRead(ctx, c, flags, "contacts", true, "/contacts", nil, nil); err != nil {
		t.Fatalf("priming read failed: %v", err)
	}
	srv.Close() // API now down

	// --no-cache: even though key-X has a populated scoped cache, the down API
	// must surface as an error, not a cached read.
	noCacheFlags := &rootFlags{dataSource: "auto", noCache: true}
	cDown := newTestClient(url, "key-X")
	if _, _, err := resolveRead(ctx, cDown, noCacheFlags, "contacts", true, "/contacts", nil, nil); err == nil {
		t.Fatal("--no-cache auto read should error when API is down, not serve cached data")
	}
}
