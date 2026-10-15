package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// PATCH /dialer/settings (app main ce2d3cdff, 2026-10-15): the typed body now carries wrapupAutoReleaseSeconds (0 = off, otherwise 30..3600).
// spec.yaml holds the typed schema, so the generated command takes --wrapup-auto-release-seconds. The API owns the 0-or-30..3600 rule (400).
func runDialerPatchSettings(t *testing.T, args ...string) (body map[string]any, hits int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != "PATCH" || r.URL.Path != "/api/v1/dialer/settings" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body is not JSON: %q", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL+"/api/v1")
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	cmd := newDialerPatchSettingsCmd(&rootFlags{noCache: true})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dialer patch-settings %v: %v", args, err)
	}
	return
}

func TestDialerPatchSettingsWrapupSeconds(t *testing.T) {
	for _, v := range []string{"0", "30", "300", "3600"} {
		body, hits := runDialerPatchSettings(t, "--wrapup-auto-release-seconds", v)
		if hits != 1 {
			t.Fatalf("%s: %d requests", v, hits)
		}
		want := map[string]any{"0": 0.0, "30": 30.0, "300": 300.0, "3600": 3600.0}[v]
		if len(body) != 1 || body["wrapupAutoReleaseSeconds"] != want {
			t.Errorf("%s: body = %v, want only wrapupAutoReleaseSeconds=%v (0 must be sent, not dropped)", v, body, want)
		}
	}
}

func TestDialerPatchSettingsWrapupRidesWithTheOtherFields(t *testing.T) {
	body, _ := runDialerPatchSettings(t, "--pause-seconds", "10", "--advance-on", "timer", "--wrapup-auto-release-seconds", "120")
	if body["pauseSeconds"] != 10.0 || body["advanceOn"] != "timer" || body["wrapupAutoReleaseSeconds"] != 120.0 || len(body) != 3 {
		t.Errorf("body = %v", body)
	}
}

func TestDialerPatchSettingsWrapupFlagIsDocumented(t *testing.T) {
	f := newDialerPatchSettingsCmd(&rootFlags{}).Flags().Lookup("wrapup-auto-release-seconds")
	if f == nil {
		t.Fatal("flag missing")
	}
	if f.Usage != "Seconds of continuous wrap-up after which a rep returns to Available automatically: 0 = off, otherwise 30 to 3600 (default 300)" {
		t.Errorf("usage = %q", f.Usage)
	}
}

func TestToolsManifestCarriesWrapupSeconds(t *testing.T) {
	raw, err := os.ReadFile("../../tools-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tools []struct {
			Name   string                                  `json:"name"`
			Params []struct{ Name, Type, Location string } `json:"params"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, tool := range m.Tools {
		if tool.Name != "dialer_patch-settings" {
			continue
		}
		for _, p := range tool.Params {
			if p.Name == "wrapupAutoReleaseSeconds" && p.Type == "int" && p.Location == "body" {
				return
			}
		}
	}
	t.Error("dialer_patch-settings has no int body param wrapupAutoReleaseSeconds in tools-manifest.json")
}
