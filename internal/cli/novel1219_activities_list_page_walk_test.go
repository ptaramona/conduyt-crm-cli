// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// PATCH(novel1219-activities-list-page-number): Greptile PR #2450 thread 4514458610. GET /activities pages by number
// (meta.page/per_page/total/has_more, no next cursor), so `activities list --all` must walk page=1, page=2, ... and
// not stop after page one waiting for a cursor the endpoint never returns.
func TestNovel1219ActivitiesListAllWalksEveryNumberedPage(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/activities" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		q := r.URL.Query()
		mu.Lock()
		pages = append(pages, q.Get("page"))
		mu.Unlock()
		if q.Get("cursor") != "" {
			t.Errorf("activities list sent cursor=%q; the endpoint pages by number", q.Get("cursor"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch q.Get("page") {
		case "", "1":
			_, _ = io.WriteString(w, `{"data":[{"id":"act-p1-a"},{"id":"act-p1-b"}],"meta":{"page":1,"per_page":2,"total":3,"has_more":true}}`)
		case "2":
			_, _ = io.WriteString(w, `{"data":[{"id":"act-p2-c"}],"meta":{"page":2,"per_page":2,"total":3,"has_more":false}}`)
		default:
			_, _ = io.WriteString(w, `{"data":[],"meta":{"page":3,"per_page":2,"total":3,"has_more":false}}`)
		}
	}))
	defer srv.Close()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL+"/api/v1")
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("CONDUYT_CRM_NO_LEARN", "true")
	t.Setenv("HOME", t.TempDir())

	root := newRootCmd(&rootFlags{})
	var out bytes.Buffer
	root.SetArgs([]string{"activities", "list", "--all", "--per-page", "2", "--data-source", "live", "--no-cache", "--json"})
	root.SetOut(&out)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatalf("activities list --all: %v", err)
	}
	for _, id := range []string{"act-p1-a", "act-p1-b", "act-p2-c"} {
		if !strings.Contains(out.String(), id) {
			t.Errorf("activities list --all output is missing %s (pages requested %q); output: %s", id, pages, out.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pages) != 2 || pages[1] != "2" {
		t.Fatalf("pages requested = %q, want page one then page=2", pages)
	}
}
