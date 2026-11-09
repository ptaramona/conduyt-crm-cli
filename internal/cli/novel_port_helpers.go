// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// PATCH(novel1219-helpers): response-shape and pagination helpers the hand-written novel commands share
// (send-check, imports blame, imports watch, contacts verify-line-type). They lived in the public library
// entry's helpers.go and dialer_coverage.go; the 4.34.2 reprint regenerated those files without them, so
// they are carried here in one hand-authored file a reprint keeps.

// paginationMaxPages is the common hard ceiling for unbounded novel-command pagination.
const paginationMaxPages = 100

type paginationProgressGuard struct {
	pages       int
	seenIDs     map[string]struct{}
	seenCursors map[string]struct{}
}

func newPaginationProgressGuard() *paginationProgressGuard {
	return &paginationProgressGuard{seenIDs: map[string]struct{}{}, seenCursors: map[string]struct{}{}}
}

// observe records one page. It fails when a full page added no new ids (the server repeats a page), when a cursor
// repeats, or when the page cap is reached while more pages remain.
func (g *paginationProgressGuard) observe(ids []string, nextCursor string, more bool) error {
	g.pages++
	added := 0
	identifiable := 0
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		identifiable++
		if _, exists := g.seenIDs[id]; exists {
			continue
		}
		g.seenIDs[id] = struct{}{}
		added++
	}
	if more && identifiable > 0 && added == 0 {
		return fmt.Errorf("pagination made no progress on page %d (zero new ids)", g.pages)
	}
	if nextCursor != "" {
		if _, exists := g.seenCursors[nextCursor]; exists {
			return fmt.Errorf("pagination cursor %q repeated on page %d", nextCursor, g.pages)
		}
		g.seenCursors[nextCursor] = struct{}{}
	}
	if more && g.pages >= paginationMaxPages {
		return fmt.Errorf("pagination reached the %d-page safety cap", paginationMaxPages)
	}
	return nil
}

func objectPaginationIDs(items []map[string]any) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strAny(item, "id", "contactId", "contact_id", "uuid"); id != "" {
			ids = append(ids, id)
			continue
		}
		ids = append(ids, "")
	}
	return ids
}

// rejectResponseErrorEnvelope refuses a 2xx body that is really an error envelope, at the root or in nested data.
func rejectResponseErrorEnvelope(raw json.RawMessage) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	// Any non-null error value marks an error envelope, regardless of its JSON
	// type. A non-empty string or object message does the same at the response
	// root and in data envelopes; null and empty-string messages remain valid.
	for depth := 0; envelope != nil; depth++ {
		if value, ok := envelope["error"]; ok && string(bytes.TrimSpace(value)) != "null" {
			return fmt.Errorf("unexpected error envelope")
		}
		if value, ok := envelope["message"]; ok {
			var text string
			if json.Unmarshal(value, &text) == nil && text != "" {
				return fmt.Errorf("unexpected message envelope: %s", text)
			}
			var detail map[string]json.RawMessage
			if json.Unmarshal(value, &detail) == nil && detail != nil {
				return fmt.Errorf("unexpected message envelope")
			}
		}
		data, ok := envelope["data"]
		var nested map[string]json.RawMessage
		if !ok || json.Unmarshal(data, &nested) != nil {
			break
		}
		envelope = nested
	}
	return nil
}

// hoistPaginatedEnvelope turns {"data":{"data":[...],"meta":{...}}} into {"data":[...],"meta":{...}}.
func hoistPaginatedEnvelope(raw json.RawMessage) (json.RawMessage, bool) {
	var outer map[string]json.RawMessage
	if json.Unmarshal(raw, &outer) != nil || len(outer) != 1 {
		return raw, false
	}
	innerRaw, ok := outer["data"]
	if !ok {
		return raw, false
	}
	var inner map[string]json.RawMessage
	if json.Unmarshal(innerRaw, &inner) != nil || len(inner) != 2 {
		return raw, false
	}
	data, dataOK := inner["data"]
	meta, metaOK := inner["meta"]
	if !dataOK || !metaOK {
		return raw, false
	}
	var rows []json.RawMessage
	var metadata map[string]json.RawMessage
	if json.Unmarshal(data, &rows) != nil || json.Unmarshal(meta, &metadata) != nil || metadata == nil {
		return raw, false
	}
	hoisted, err := json.Marshal(map[string]json.RawMessage{"data": data, "meta": meta})
	if err != nil {
		return raw, false
	}
	return hoisted, true
}
