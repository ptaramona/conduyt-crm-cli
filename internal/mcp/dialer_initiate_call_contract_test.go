package mcp

// PATCH(dialer-initiate-call-contract-pin-930): the MCP runtime path for dialer.initiate-call (board #930). The binary registers the
// generic conduyt-crm_execute tool; for a POST the params that are not path/query bindings become the JSON body. Drive that real
// path with every request-schema key and require POST /dialer/call with exactly those keys in the body. The list is hard-coded
// (see internal/cli/dialer_initiate_call_contract_test.go, which also compares it to spec.yaml and upstream/openapi.yaml).

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestCodeOrchInitiateCallSendsExactlySchemaKeys(t *testing.T) {
	want := []string{"autoDialer", "callerIdStrategy", "contactId", "dncOverride", "to", "vendorBridgeNonce", "vendorBridgeable"}
	params := map[string]any{
		"to": "+15555550100", "contactId": "00000000-0000-0000-0000-000000000000", "dncOverride": true, "autoDialer": true,
		"callerIdStrategy": "did", "vendorBridgeNonce": "n", "vendorBridgeable": true,
	}
	hits, body, rawQuery, res := execGuard(t, "dialer.initiate-call", params)
	if hits != 1 || res.IsError {
		t.Fatalf("hits=%d isError=%v", hits, res.IsError)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	var keys []string
	for k := range sent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("MCP body keys %v != schema properties %v", keys, want)
	}
	if rawQuery != "" {
		t.Errorf("no body key may move to the query string, got %q", rawQuery)
	}
}

func TestCodeOrchInitiateCallRoute(t *testing.T) {
	for _, ep := range codeOrchEndpoints {
		if ep.ID == "dialer.initiate-call" {
			if ep.Method != "POST" || ep.Path != "/dialer/call" || len(ep.QueryParams) != 0 || len(ep.TemplateParams) != 0 {
				t.Errorf("route = %s %s query=%v template=%v", ep.Method, ep.Path, ep.QueryParams, ep.TemplateParams)
			}
			return
		}
	}
	t.Fatal("dialer.initiate-call missing from codeOrchEndpoints")
}
