package cli

// PATCH(reprint-637-required-on-changed): every REQUIRED scalar body field in tools-manifest.json, passed with its zero
// value ("" / 0 / 0.0 / false), appears in the body the command would send. The table is the manifest itself (one row
// per tool x required body param), so a reprint that brings back the generator's `!= ""` / `!= 0` guard fails here
// until .printing-press-patches/required-on-changed.pl is run again.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/spf13/cobra"
)

type manifestParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location string `json:"location"`
	Required bool   `json:"required"`
}

type manifestTool struct {
	Name   string          `json:"name"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Params []manifestParam `json:"params"`
}

func kebabFlagName(param string) string {
	var b strings.Builder
	for i, r := range param {
		switch {
		case r == '_':
			b.WriteRune('-')
		case unicode.IsUpper(r):
			if i > 0 {
				b.WriteRune('-')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// previewBody returns the top-level keys of the JSON body in a dry-run preview.
func previewBody(preview string) (map[string]any, bool) {
	_, rest, ok := strings.Cut(preview, "  Body:\n")
	if !ok {
		return nil, false
	}
	var body map[string]any
	if err := json.NewDecoder(strings.NewReader(rest)).Decode(&body); err != nil {
		return nil, false
	}
	return body, true
}

// requiredZeroValueExceptions: required body flags whose zero value is refused locally on purpose (each by its own
// ledger entry), so it is never serialized. Every other required scalar body flag must reach the body.
var requiredZeroValueExceptions = map[string]string{
	"conduyt-auth register --terms-accepted":                 "register-terms-accepted-601: --terms-accepted=false is a refusal",
	"contacts duplicate-proposals decide-contact --decision": "duplicate-proposals-look-board-95: decision must be one of not_sure, different, merge",
	"email send --to":                     "email-send-live-keys-cli-324: hand-written required set (to, contact-id, subject) checked together",
	"email send --contact-id":             "email-send-live-keys-cli-324: hand-written required set",
	"email send --subject":                "email-send-live-keys-cli-324: hand-written required set",
	"call-flows post-audio --file":        "multipart file path: an empty path cannot be uploaded (local open error, nothing sent)",
	"users avatar post-users-id --avatar": "multipart file path: an empty path cannot be uploaded (local open error, nothing sent)",
}

// dryRunBody runs the command under the global --dry-run with extra flags and returns the previewed JSON body as text.
func dryRunBody(t *testing.T, cmd *cobra.Command, extra ...string) (string, error) {
	t.Helper()
	root := newRootCmd(&rootFlags{})
	args := strings.Fields(strings.TrimPrefix(cmd.CommandPath(), root.Name()+" "))
	for i := 0; i < strings.Count(cmd.Use, "<"); i++ {
		args = append(args, "00000000-0000-4000-8000-00000000000"+string(rune('1'+i)))
	}
	args = append(append(args, extra...), "--dry-run", "--no-cache", "--yes")
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	var runErr error
	preview := captureStderr(t, func() { runErr = root.Execute() })
	if runErr != nil {
		return "", runErr
	}
	body, ok := previewBody(preview)
	if !ok {
		return "", nil
	}
	b, _ := json.Marshal(body)
	return string(b), nil
}

func TestEveryRequiredBodyFlagSerializesItsZeroValue(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Tools []manifestTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	byOp := map[string]*cobra.Command{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if m, p := c.Annotations["pp:method"], c.Annotations["pp:path"]; m != "" && p != "" {
			if _, seen := byOp[m+" "+p]; !seen {
				byOp[m+" "+p] = c
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd(&rootFlags{}))

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer srv.Close()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL)
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("CONDUYT_CRM_NO_LEARN", "true")
	t.Setenv("HOME", t.TempDir())

	zero := map[string]string{"string": "", "int": "0", "integer": "0", "number": "0", "float": "0", "bool": "false", "boolean": "false"}
	checked, skipped := 0, 0
	var unmapped []string
	for _, tool := range manifest.Tools {
		cmd := byOp[tool.Method+" "+tool.Path]
		if cmd == nil || tool.Method == "GET" || tool.Method == "DELETE" {
			continue
		}
		for _, p := range tool.Params {
			z, scalar := zero[p.Type]
			if !p.Required || p.Location != "body" || !scalar || strings.Contains(p.Name, ".") {
				continue
			}
			flagName := kebabFlagName(p.Name)
			if cmd.Flags().Lookup(flagName) == nil {
				unmapped = append(unmapped, tool.Name+"."+p.Name)
				continue
			}
			key := cmd.CommandPath() + " --" + flagName
			if _, skip := requiredZeroValueExceptions[strings.TrimPrefix(key, "conduyt-crm-pp-cli ")]; skip {
				skipped++
				continue
			}
			with, errWith := dryRunBody(t, cmd, "--"+flagName+"="+z)
			if errWith != nil {
				t.Errorf("%s=%q --dry-run: %v", key, z, errWith)
				continue
			}
			without, _ := dryRunBody(t, cmd)
			if with == without {
				t.Errorf("%s=%q: the required body field %q is not in the body (%s)", key, z, p.Name, with)
			}
			checked++
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("--dry-run sent %d request(s)", n)
	}
	sort.Strings(unmapped)
	t.Logf("checked %d required scalar body params, %d documented exceptions; %d without a same-named flag (renamed or hand-shaped): %v", checked, skipped, len(unmapped), unmapped)
	if checked < 200 {
		t.Fatalf("only %d required body params checked: the manifest walk is broken", checked)
	}
}
