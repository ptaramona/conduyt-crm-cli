// Copyright 2026 conduyt. Licensed under Apache-2.0. See LICENSE.
// PATCH: hand-authored regression tests for the v1.1.3 dogfood bug fixes
// (nested-envelope unwrap, meta-driven pagination, standardized data key).
// Not generated — see .printing-press-patches.json.

package cli

import (
	"encoding/json"
	"testing"
)

// nestedDealsEnvelope mirrors the live Conduyt /deals shape:
// {"data": {"data": [...], "meta": {...}}}.
const nestedDealsEnvelope = `{"data":{"data":[{"id":"a"},{"id":"b"}],"meta":{"page":1,"per_page":50,"total":2}}}`

func TestUnwrapAPIData_NestedPaginated(t *testing.T) {
	flat, meta := unwrapAPIData(json.RawMessage(nestedDealsEnvelope))
	var items []map[string]any
	if err := json.Unmarshal(flat, &items); err != nil {
		t.Fatalf("flattened payload is not an array: %v (got %s)", err, flat)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if meta == nil || meta["total"] != float64(2) {
		t.Fatalf("want pagination meta total=2, got %v", meta)
	}
}

func TestUnwrapAPIData_SingleObject(t *testing.T) {
	// Detail response: {"data": {<object>}}
	flat, meta := unwrapAPIData(json.RawMessage(`{"data":{"id":"x","title":"T"}}`))
	var obj map[string]any
	if err := json.Unmarshal(flat, &obj); err != nil {
		t.Fatalf("payload not an object: %v", err)
	}
	if obj["id"] != "x" {
		t.Fatalf("want id=x, got %v", obj["id"])
	}
	if meta != nil {
		t.Fatalf("detail response should carry no pagination meta, got %v", meta)
	}
}

// TestUnwrapAPIData_DetailWithDataSiblingPreserved is the Finding 2 regression:
// a detail resource that legitimately owns a "data" field alongside sibling
// fields must NOT be reshaped to only its inner "data". The flattener used to
// treat any inner object with a "data" key as a paginated envelope and drop the
// siblings ("event" here), corrupting the no-drop standardization guarantee.
func TestUnwrapAPIData_DetailWithDataSiblingPreserved(t *testing.T) {
	// {"data":{"event":"x","data":{...}}} — inner "data" is an object, no meta,
	// and there is a sibling "event". The whole inner resource must survive.
	in := json.RawMessage(`{"data":{"event":"contact.created","id":"wh_1","data":{"contact_id":"c_9","name":"Ada"}}}`)
	flat, meta := unwrapAPIData(in)
	if meta != nil {
		t.Fatalf("detail response should carry no pagination meta, got %v", meta)
	}
	var obj map[string]any
	if err := json.Unmarshal(flat, &obj); err != nil {
		t.Fatalf("payload not an object: %v (got %s)", err, flat)
	}
	if obj["event"] != "contact.created" {
		t.Fatalf("sibling field 'event' was dropped: got %v (full %s)", obj["event"], flat)
	}
	if obj["id"] != "wh_1" {
		t.Fatalf("sibling field 'id' was dropped: got %v (full %s)", obj["id"], flat)
	}
	inner, ok := obj["data"].(map[string]any)
	if !ok {
		t.Fatalf("inner 'data' object was lost or reshaped: got %T (full %s)", obj["data"], flat)
	}
	if inner["contact_id"] != "c_9" {
		t.Fatalf("inner data.contact_id mangled: got %v", inner["contact_id"])
	}
}

// TestUnwrapAPIData_EnvelopeOnlyDataMetaFlattened guards the legitimate
// flatten path: an inner object whose ONLY keys are data/meta is a genuine
// envelope and should still be flattened to its items even without an array.
func TestUnwrapAPIData_EnvelopeOnlyDataMetaFlattened(t *testing.T) {
	in := json.RawMessage(`{"data":{"data":[{"id":"a"}],"meta":{"total":1}}}`)
	flat, meta := unwrapAPIData(in)
	var items []map[string]any
	if err := json.Unmarshal(flat, &items); err != nil {
		t.Fatalf("want flattened array, got %s (%v)", flat, err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if meta == nil || meta["total"] != float64(1) {
		t.Fatalf("want meta total=1, got %v", meta)
	}
}

func TestUnwrapAPIData_BareArrayUnchanged(t *testing.T) {
	in := json.RawMessage(`[{"id":"a"}]`)
	flat, meta := unwrapAPIData(in)
	if string(flat) != string(in) {
		t.Fatalf("bare array should pass through unchanged, got %s", flat)
	}
	if meta != nil {
		t.Fatalf("bare array carries no meta, got %v", meta)
	}
}

func TestExtractPaginatedItems_NestedData(t *testing.T) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(nestedDealsEnvelope), &obj); err != nil {
		t.Fatal(err)
	}
	items, ok := extractPaginatedItems(obj)
	if !ok || len(items) != 2 {
		t.Fatalf("want 2 nested items, ok=%v len=%d", ok, len(items))
	}
}

func TestExtractPaginationMeta_Nested(t *testing.T) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(nestedDealsEnvelope), &obj); err != nil {
		t.Fatal(err)
	}
	page, perPage, total, found := extractPaginationMeta(obj)
	if !found || page != 1 || perPage != 50 || total != 2 {
		t.Fatalf("want page=1 per_page=50 total=2 found, got %d %d %d %v", page, perPage, total, found)
	}
}

func TestWrapWithProvenance_StandardDataKey(t *testing.T) {
	wrapped, err := wrapWithProvenance(json.RawMessage(nestedDealsEnvelope), DataProvenance{Source: "live"})
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(wrapped, &env); err != nil {
		t.Fatal(err)
	}
	// payload must be exposed at "data" (canonical) and "results" (alias)
	var data []map[string]any
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("envelope.data is not the items array: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("want 2 rows at envelope.data, got %d", len(data))
	}
	if _, ok := env["results"]; !ok {
		t.Fatal("results alias missing for back-compat")
	}
}
