// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ptaramona/conduyt-crm-cli/internal/client"
	"github.com/ptaramona/conduyt-crm-cli/internal/store"
)

// PATCH(novel1219-activities-list-page-number): Codex novel1219-r19 (2 P2). normalizePageContinuation is the one
// page-numbered continuation rule for the flat and the dependent sync loops: a non-empty body continuation proves a
// next page whatever the page's length, a numeric one is used as is, and a non-numeric one never reaches page=.

const novel1219Keyset = "WyIyMDI2LTExLTA5VDE3OjQwOjQ5LjI1NVoiLCJjaGlsZC0yIl0"

// novel1219PageAPI answers page 1 with firstRows rows and the given continuation token, page 2 with 2 rows and no
// token, and any non-integer page with a 500-equivalent error, like GET /activities.
type novel1219PageAPI struct {
	firstRows int
	token     string
	pages     []string
}

func (a *novel1219PageAPI) Get(_ context.Context, _ string, params map[string]string) (json.RawMessage, error) {
	p := params["page"]
	a.pages = append(a.pages, p)
	if _, err := strconv.Atoi(p); p != "" && err != nil {
		return nil, &client.APIError{Method: "GET", Path: "/parents/p1/children", StatusCode: 500, Body: `{"error":"Internal server error"}`}
	}
	rows := func(page, n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = `{"id":"child-` + strconv.Itoa(page) + `-` + strconv.Itoa(i) + `"}`
		}
		return strings.Join(out, ",")
	}
	switch p {
	case "", "1":
		next := "null"
		if a.token != "" {
			next = strconv.Quote(a.token)
		}
		return json.RawMessage(`{"data":[` + rows(1, a.firstRows) + `],"meta":{"next_cursor":` + next + `}}`), nil
	case "2":
		return json.RawMessage(`{"data":[` + rows(2, 2) + `],"meta":{"next_cursor":null}}`), nil
	}
	return json.RawMessage(`{"data":[],"meta":{"next_cursor":null}}`), nil
}

func (a *novel1219PageAPI) RateLimit() float64 { return 0 }

// The dependent is named activities only because resourceSupportsPagination must hold; the route and pageSize are the test's own.
func novel1219RunDependent(t *testing.T, api *novel1219PageAPI) parentReport {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dep := dependentResourceDef{Name: "activities", ParentTable: "parents", PathTemplate: "/parents/{id}/children", PathParams: []dependentPathParamDef{{Param: "id", Field: "id"}}}
	pageSize := paginationDefaults{cursorParam: "page", cursorType: "page", initialCursor: "1", limitParam: "per_page", limit: 3}
	return syncOneParent(context.Background(), api, db, dep, map[string]string{"id": "p1"}, dep.PathParams, pageSize, "", "", 0, false, false, nil, io.Discard, "")
}

func novel1219AssertTwoPages(t *testing.T, api *novel1219PageAPI, rep parentReport, wantStored int) {
	t.Helper()
	for _, p := range api.pages {
		if _, err := strconv.Atoi(p); p != "" && err != nil {
			t.Fatalf("a page carried a non-numeric value: pages %q", api.pages)
		}
	}
	if rep.failure != nil || rep.stored != wantStored || !rep.complete || len(api.pages) < 2 || api.pages[1] != "2" {
		t.Fatalf("stored %d complete %v failure %v reason %q pages %q; want %d stored over page one then page=2", rep.stored, rep.complete, rep.failure, rep.reason, api.pages, wantStored)
	}
}

// P2-1, dependent loop, full page with an opaque token: the next request is page=2, not page=<token>.
func TestNovel1219DependentFullPageWithOpaqueTokenAdvancesByNumber(t *testing.T) {
	api := &novel1219PageAPI{firstRows: 3, token: novel1219Keyset}
	novel1219AssertTwoPages(t, api, novel1219RunDependent(t, api), 5)
}

// P2-1, dependent loop, short page with an opaque token: not the end.
func TestNovel1219DependentShortPageWithOpaqueTokenAdvancesByNumber(t *testing.T) {
	api := &novel1219PageAPI{firstRows: 1, token: novel1219Keyset}
	novel1219AssertTwoPages(t, api, novel1219RunDependent(t, api), 3)
}

// P2-2, dependent loop, short page with a numeric continuation: not the end either.
func TestNovel1219DependentShortPageWithNumericTokenAdvances(t *testing.T) {
	api := &novel1219PageAPI{firstRows: 1, token: "2"}
	novel1219AssertTwoPages(t, api, novel1219RunDependent(t, api), 3)
}

// P2-2, flat loop: a short page whose continuation is the numeric next page goes on to that page.
func TestNovel1219SyncShortPageWithNumericTokenAdvances(t *testing.T) {
	s := &novel1219SyncServer{firstRows: 3, token: true, numericToken: true}
	dir := novel1219SyncEnv(t, s.handler(t))
	out, err := novel1219Sync(t, filepath.Join(dir, "a.db"), "--full")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		t.Fatalf("sync: %v (requests %q)", err, s.reqs)
	}
	if !strings.Contains(out, `"total_records": 5`) || len(s.reqs) < 2 || !strings.Contains(s.reqs[1], "page=2") {
		t.Fatalf("a short page with next_cursor \"2\" ended the walk (requests %q): %s", s.reqs, out)
	}
}

func TestNovel1219NormalizePageContinuation(t *testing.T) {
	page := paginationDefaults{cursorParam: "page", cursorType: "page"}
	cases := []struct {
		cursor, next     string
		hasMore          bool
		wantNext         string
		wantMore, proved bool
	}{
		{"", novel1219Keyset, false, "2", true, true},
		{"3", novel1219Keyset, false, "4", true, true},
		{"1", "2", false, "2", true, true},
		{"1", "", false, "", false, false},
		{"1", "", true, "", true, false},
	}
	for _, tc := range cases {
		next, more, proved := normalizePageContinuation(page, tc.cursor, tc.next, tc.hasMore)
		if next != tc.wantNext || more != tc.wantMore || proved != tc.proved {
			t.Errorf("normalize(cursor %q, next %q, more %v) = %q %v %v; want %q %v %v", tc.cursor, tc.next, tc.hasMore, next, more, proved, tc.wantNext, tc.wantMore, tc.proved)
		}
	}
	cursorType := paginationDefaults{cursorParam: "cursor", cursorType: "cursor"}
	if next, more, proved := normalizePageContinuation(cursorType, "", novel1219Keyset, true); next != novel1219Keyset || !more || proved {
		t.Errorf("cursor pagination must pass its token through unchanged; got %q %v %v", next, more, proved)
	}
}
