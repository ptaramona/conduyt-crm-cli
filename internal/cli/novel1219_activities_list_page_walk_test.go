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
// not stop after page one. The fixture is the live response shape: a data envelope around data + meta (the spec's
// ActivitiesListResponse), whose meta.next_cursor is an opaque string that must never be sent as page=.
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
		if p := q.Get("page"); p != "" && p != "1" && p != "2" && p != "3" {
			t.Errorf("activities list sent page=%q, not a page number", p)
		}
		w.Header().Set("Content-Type", "application/json")
		switch q.Get("page") {
		case "", "1":
			_, _ = io.WriteString(w, `{"data":{"data":[{"id":"act-p1-a"},{"id":"act-p1-b"}],"meta":{"page":1,"per_page":2,"total":3,"total_capped":false,"next_cursor":"WyIyMDI2LTExLTA5VDE3OjQwOjQ5LjI1NVoiLCJhY3QtcDEtYiJd"}}}`)
		case "2":
			_, _ = io.WriteString(w, `{"data":{"data":[{"id":"act-p2-c"}],"meta":{"page":2,"per_page":2,"total":3,"total_capped":false,"next_cursor":null}}}`)
		default:
			_, _ = io.WriteString(w, `{"data":{"data":[],"meta":{"page":3,"per_page":2,"total":3,"total_capped":false,"next_cursor":null}}}`)
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

// PATCH(novel1219-activities-list-page-number): live, GET /activities also returns meta.next_cursor (an opaque string).
// The page-numbered sync must not send that string as page= (the API answers 500); it advances by number.
func TestNovel1219ActivitiesSyncWalksNumberedPagesPastAnOpaqueNextCursor(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		pages = append(pages, q.Get("page"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		rows := func(page, n int) string {
			out := make([]string, n)
			for i := range out {
				out[i] = `{"id":"act-p` + string(rune('0'+page)) + `-` + string(rune('a'+i)) + `","type":"note","createdAt":"2026-11-0` + string(rune('0'+9-page)) + `T00:00:00Z"}`
			}
			return strings.Join(out, ",")
		}
		switch q.Get("page") {
		case "", "1":
			_, _ = io.WriteString(w, `{"data":{"data":[`+rows(1, 50)+`],"meta":{"page":1,"per_page":50,"total":53,"total_capped":false,"next_cursor":"WyIyMDI2LTExLTA5VDE3OjQwOjQ5LjI1NVoiLCJhY3QtcDEtYSJd"}}}`)
		case "2":
			_, _ = io.WriteString(w, `{"data":{"data":[`+rows(2, 3)+`],"meta":{"page":2,"per_page":50,"total":53,"total_capped":false,"next_cursor":null}}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"Internal server error"}`)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL+"/api/v1")
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("CONDUYT_CRM_NO_LEARN", "true")
	t.Setenv("HOME", dir)

	root := newRootCmd(&rootFlags{})
	var out bytes.Buffer
	root.SetArgs([]string{"sync", "--resources", "activities", "--full", "--db", filepath.Join(dir, "a.db"), "--json"})
	root.SetOut(&out)
	root.SetErr(io.Discard)
	err := root.Execute()
	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		t.Fatalf("sync activities: %v (pages requested %q)", err, pages)
	}
	if len(pages) != 2 || pages[1] != "2" {
		t.Fatalf("pages requested = %q, want page one then page=2", pages)
	}
	if !strings.Contains(out.String(), `"total_records": 53`) {
		t.Fatalf("sync summary does not report 53 records: %s", out.String())
	}
}
