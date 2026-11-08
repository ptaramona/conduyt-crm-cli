package cli

// Codex reprint637-r1 P2s: the 11-08 reprint renamed two commands and dropped two conditional requirements.
// PATCH(reprint-637-renamed-command-aliases), PATCH(reprint-637-conditional-requirements).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recordedRequest struct {
	Method, Path, Query, Body string
}

func recordingServer(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recordedRequest{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), got...)
	}
}

func runCLI(t *testing.T, srvURL string, args ...string) error {
	t.Helper()
	isolateCLIEnv(t)
	t.Setenv("CONDUYT_CRM_BASE_URL", srvURL)
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "cdy_test_reprint637")
	root := newRootCmd(&rootFlags{})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append(args, "--json", "--no-cache"))
	return root.Execute()
}

func TestBareAccountExportsStillListsThem(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		query string
	}{
		{[]string{"account-exports"}, ""},
		{[]string{"account-exports", "--cursor", "c-42"}, "cursor=c-42"},
		{[]string{"account-exports", "get", "--cursor", "c-42"}, "cursor=c-42"},
	} {
		srv, reqs := recordingServer(t)
		if err := runCLI(t, srv.URL, tc.args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		got := reqs()
		if len(got) == 0 || got[0].Method != "GET" || !strings.HasSuffix(got[0].Path, "/account-exports") || !strings.Contains(got[0].Query, tc.query) {
			t.Fatalf("%v sent %+v, want GET /account-exports?%s", tc.args, got, tc.query)
		}
	}
}

func TestRenamedDncCommandKeepsItsOldName(t *testing.T) {
	root := newRootCmd(&rootFlags{})
	oldCmd, _, err := root.Find([]string{"contacts", "dnc", "get-contacts-id"})
	if err != nil {
		t.Fatal(err)
	}
	newCmd, _, _ := root.Find([]string{"contacts", "dnc", "get-contact-verdicts"})
	if oldCmd != newCmd || newCmd.Annotations["pp:path"] != "/contacts/{id}/dnc" {
		t.Fatalf("contacts dnc get-contacts-id resolves to %q, want get-contact-verdicts", oldCmd.CommandPath())
	}
}

