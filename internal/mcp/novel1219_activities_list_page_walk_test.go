// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// PATCH(novel1219-activities-list-page-number): Greptile PR #2450 thread 4514458610. The MCP activities.list endpoint
// must advertise and accept the page number as its cursor param, because GET /activities returns no next cursor. An
// agent that follows cursor_param from conduyt-crm_get must walk a two-page fixture completely.
func TestNovel1219MCPActivitiesListWalksEveryNumberedPage(t *testing.T) {
	ep := findCodeOrchEndpoint("activities.list")
	if ep == nil {
		t.Fatal("activities.list is not registered")
	}
	cursorParam, _ := codeOrchEndpointMetadata(ep)["cursor_param"].(string)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
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

	seen := map[string]bool{}
	params := map[string]any{"per_page": float64(2)}
	for call := 1; call <= 4; call++ {
		res, err := codeOrchTestInvoke(context.Background(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{
			Arguments: map[string]any{"endpoint_id": "activities.list", "params": params},
		}})
		if err != nil || res.IsError {
			t.Fatalf("call %d: err=%v result=%+v", call, err, res)
		}
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(mcplib.TextContent); ok {
				text += tc.Text
			}
		}
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Meta struct {
				Page    int  `json:"page"`
				HasMore bool `json:"has_more"`
			} `json:"meta"`
		}
		if err := json.Unmarshal([]byte(text), &page); err != nil {
			t.Fatalf("call %d: decode %q: %v", call, text, err)
		}
		for _, row := range page.Data {
			seen[row.ID] = true
		}
		if !page.Meta.HasMore {
			break
		}
		params = map[string]any{"per_page": float64(2), cursorParam: float64(page.Meta.Page + 1)}
	}
	for _, id := range []string{"act-p1-a", "act-p1-b", "act-p2-c"} {
		if !seen[id] {
			t.Errorf("MCP walk following cursor_param %q missed %s (saw %v)", cursorParam, id, seen)
		}
	}
	if ep.Page.CursorParam != "page" || ep.Page.NextCursorPath != "" {
		t.Errorf("activities.list page config = %+v, want page numbering with no next-cursor path", ep.Page)
	}
}
