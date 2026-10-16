package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Dialer unknown-caller prompt (board #35, app main f132ed6f0, 2026-10-15): POST /calls/{id}/link, GET /calls/unknown-callers (the
// toll-free counter), GET /dialer/history?unmatched=&line= and POST /contacts with callId. spec.yaml carries all four from the public OpenAPI.
type seenReq struct {
	method string
	path   string
	query  url.Values
	body   map[string]any
}

func runUnknownCaller(t *testing.T, build func(*rootFlags) *cobra.Command, args ...string) (reqs []seenReq, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rq := seenReq{method: r.Method, path: r.URL.Path, query: r.URL.Query()}
		if len(raw) > 0 {
			rq.body = map[string]any{}
			_ = json.Unmarshal(raw, &rq.body)
		}
		reqs = append(reqs, rq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total":14,"unmatched":9}}`))
	}))
	defer srv.Close()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL+"/api/v1")
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	cmd := build(&rootFlags{noCache: true})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return
}

const (
	ucCall    = "9b2d7c1a-5e4f-4a3b-9c8d-7e6f5a4b3c2d"
	ucContact = "3f8a0c4e-1111-4222-8333-444455556666"
)

func TestCallsCountUnknownCallersSendsLineAndSince(t *testing.T) {
	reqs, err := runUnknownCaller(t, newCallsCountUnknownCallersCmd, "--line", "+18005550199", "--since", "2026-10-15T07:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].method != "GET" || reqs[0].path != "/api/v1/calls/unknown-callers" {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[0].query.Get("line") != "+18005550199" || reqs[0].query.Get("since") != "2026-10-15T07:00:00.000Z" || len(reqs[0].query) != 2 {
		t.Errorf("query = %v", reqs[0].query)
	}
}

func TestCallsCountUnknownCallersRequiresBothFlags(t *testing.T) {
	for _, args := range [][]string{{"--line", "+18005550199"}, {"--since", "2026-10-15T07:00:00.000Z"}} {
		reqs, err := runUnknownCaller(t, newCallsCountUnknownCallersCmd, args...)
		if err == nil || !strings.Contains(err.Error(), "required flag") {
			t.Errorf("%v: err = %v, want a required-flag error", args, err)
		}
		if len(reqs) != 0 {
			t.Errorf("%v: %d requests, want none", args, len(reqs))
		}
	}
}

func TestCallsLinkCallToContactBodyShapes(t *testing.T) {
	cases := []struct {
		flags []string
		want  map[string]any
	}{
		{nil, map[string]any{"contactId": ucContact}},
		{[]string{"--attach-number"}, map[string]any{"contactId": ucContact, "attachNumber": true}},
		{[]string{"--attach-number", "--dry-run"}, map[string]any{"contactId": ucContact, "attachNumber": true, "dryRun": true}},
	}
	for _, c := range cases {
		args := append([]string{ucCall, "--contact-id", ucContact}, c.flags...)
		reqs, err := runUnknownCaller(t, newCallsLinkCallToContactCmd, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(reqs) != 1 || reqs[0].method != "POST" || reqs[0].path != "/api/v1/calls/"+ucCall+"/link" {
			t.Fatalf("%v: requests = %+v", args, reqs)
		}
		if len(reqs[0].body) != len(c.want) {
			t.Errorf("%v: body = %v, want %v", args, reqs[0].body, c.want)
		}
		for k, v := range c.want {
			if reqs[0].body[k] != v {
				t.Errorf("%v: body[%s] = %v, want %v", args, k, reqs[0].body[k], v)
			}
		}
	}
}

func TestCallsLinkCallToContactRequiresContact(t *testing.T) {
	reqs, err := runUnknownCaller(t, newCallsLinkCallToContactCmd, ucCall, "--attach-number")
	if err == nil || !strings.Contains(err.Error(), "contact-id") {
		t.Errorf("err = %v, want a contact-id required error", err)
	}
	if len(reqs) != 0 {
		t.Errorf("%d requests, want none", len(reqs))
	}
}

func TestContactsCreateCallIdRidesInTheBody(t *testing.T) {
	reqs, err := runUnknownCaller(t, newContactsCreateCmd, "--first-name", "Carla", "--call-id", ucCall)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].method != "POST" || reqs[0].path != "/api/v1/contacts" {
		t.Fatalf("requests = %+v", reqs)
	}
	b := reqs[0].body
	if len(b) != 2 || b["firstName"] != "Carla" || b["callId"] != ucCall {
		t.Errorf("body = %v, want exactly firstName and callId (no source, no owner: the server derives both)", b)
	}
}

func TestDialerGetHistoryUnmatchedAndLine(t *testing.T) {
	reqs, err := runUnknownCaller(t, newDialerGetHistoryCmd, "--unmatched", "1", "--line", "+18005550199")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].path != "/api/v1/dialer/history" || reqs[0].query.Get("unmatched") != "1" || reqs[0].query.Get("line") != "+18005550199" {
		t.Fatalf("requests = %+v", reqs)
	}
	reqs, err = runUnknownCaller(t, newDialerGetHistoryCmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs[0].query) != 0 {
		t.Errorf("bare history sent %v, want no query", reqs[0].query)
	}
}

func TestUnknownCallerCommandsAreRegistered(t *testing.T) {
	root := RootCmd()
	for _, path := range [][]string{{"calls", "count-unknown-callers"}, {"calls", "link", "call-to-contact"}} {
		if c, _, err := root.Find(path); err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Errorf("%v not registered (%v)", path, err)
		}
	}
}
