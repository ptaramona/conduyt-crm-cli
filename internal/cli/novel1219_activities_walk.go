// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// PATCH(novel1219-activities-list-page-number): GET /activities answers {"data":{"data":[...],"meta":{page, per_page,
// total, total_capped, next_cursor}}}. It takes a keyset `cursor` (the last shown row's createdAt and id) and a numeric
// `page`. `activities list --all` follows meta.next_cursor through `cursor` while one is returned, stops when it is
// null, and falls back to numeric pages only when a response carries no next_cursor key at all. A cursor is never sent
// as `page`: the route parses page as an integer and a keyset string there is a 500.

type activitiesGetter interface {
	GetWithHeaders(ctx context.Context, path string, params map[string]string, headers map[string]string) (json.RawMessage, error)
	IsDryRun() bool
}

func walkActivitiesAll(ctx context.Context, c activitiesGetter, path string, params map[string]string) (json.RawMessage, error) {
	q := map[string]string{}
	for k, v := range params {
		if v != "" {
			q[k] = v
		}
	}
	perPage := 50
	if n, err := strconv.Atoi(q["per_page"]); err == nil && n > 0 {
		perPage = n
	} else {
		q["per_page"] = strconv.Itoa(perPage)
	}
	if q["cursor"] != "" {
		delete(q, "page")
	} else if p := q["page"]; p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 {
			return nil, fmt.Errorf("--page %q is not a page number", p)
		}
	}
	rows := make([]json.RawMessage, 0)
	seen := map[string]bool{}
	for page := 1; ; page++ {
		data, err := c.GetWithHeaders(ctx, path, q, nil)
		if err != nil {
			return nil, err
		}
		if isDryRunResponse(c.IsDryRun(), data) {
			return data, nil
		}
		var env struct {
			Data *struct {
				Data []json.RawMessage          `json:"data"`
				Meta map[string]json.RawMessage `json:"meta"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &env); err != nil || env.Data == nil || env.Data.Data == nil {
			return nil, fmt.Errorf("GET %s: response is not the activities envelope {data:{data:[...],meta:{...}}}", path)
		}
		rows = append(rows, env.Data.Data...)
		raw, hasKey := env.Data.Meta["next_cursor"]
		next := ""
		if hasKey && string(raw) != "null" {
			if err := json.Unmarshal(raw, &next); err != nil {
				return nil, fmt.Errorf("GET %s: meta.next_cursor is not a string", path)
			}
		}
		switch {
		case next != "":
			if seen[next] {
				return nil, fmt.Errorf("GET %s: meta.next_cursor repeated; refusing to return a partial walk as complete", path)
			}
			seen[next] = true
			q["cursor"] = next
			delete(q, "page")
		case hasKey:
			return json.Marshal(rows)
		case len(env.Data.Data) >= perPage:
			if q["cursor"] != "" {
				return nil, fmt.Errorf("GET %s: a cursor page returned no next_cursor; cannot continue by page number", path)
			}
			current := 1
			if n, err := strconv.Atoi(q["page"]); err == nil && n > 0 {
				current = n
			}
			q["page"] = strconv.Itoa(current + 1)
		default:
			return json.Marshal(rows)
		}
		if page >= paginatedGetMaxPages*10 {
			return nil, fmt.Errorf("GET %s: more than %d pages; narrow the filters or raise --per-page", path, paginatedGetMaxPages*10)
		}
	}
}
