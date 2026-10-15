package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
)

// GET /contacts?email= (app main 318adf38d): exact, repeatable lookup on the primary and secondary email slots, up to 20.
// spec.yaml carries the param as an exploded form-style array, so the flag takes a comma list or a JSON array and the
// request carries one `email` query param per address (never one comma-joined value).
func runContactsList(t *testing.T, args ...string) (q url.Values, hits int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != "GET" || r.URL.Path != "/api/v1/contacts" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		q = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	defer srv.Close()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL+"/api/v1")
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	cmd := newContactsListCmd(&rootFlags{noCache: true})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("contacts list %v: %v", args, err)
	}
	return
}

func TestContactsListEmailFlag(t *testing.T) {
	if newContactsListCmd(&rootFlags{}).Flags().Lookup("email") == nil {
		t.Fatal("contacts list must take --email")
	}
}

func TestContactsListEmailSendsRepeatedParams(t *testing.T) {
	want := []string{"a@x.com", "b@y.com"}
	for name, arg := range map[string]string{"comma list": "a@x.com,b@y.com", "json array": `["a@x.com","b@y.com"]`} {
		q, hits := runContactsList(t, "--email", arg, "--search", "smith")
		if hits != 1 {
			t.Fatalf("%s: hits=%d", name, hits)
		}
		if got := q["email"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: email params = %q, want %q (one per address, not comma-joined)", name, got, want)
		}
		if q.Get("search") != "smith" {
			t.Fatalf("%s: search = %q", name, q.Get("search"))
		}
	}
}

func TestContactsListWithoutEmailSendsNone(t *testing.T) {
	q, _ := runContactsList(t)
	if _, ok := q["email"]; ok {
		t.Fatalf("no --email must send no email param, got %q", q["email"])
	}
}
