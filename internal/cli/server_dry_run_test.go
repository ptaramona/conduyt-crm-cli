package cli

// PATCH(reprint-637-server-dry-run): the global --dry-run means "print the request, send nothing" on every command. The API's
// dry_run query parameter is the local flag --server-dry-run (the request IS sent); no command registers a local --dry-run.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// email send and send-bulk keep their hand-written flag set (email-send-live-keys-cli-324.md): dry_run stays off their flags.
var serverDryRunExempt = map[string]bool{"POST /email/send": true, "POST /email/send-bulk": true}

func specDryRunOperations(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	resolve := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		for m != nil {
			ref, ok := m["$ref"].(string)
			if !ok {
				break
			}
			var node any = doc
			for _, k := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				node = node.(map[string]any)[k]
			}
			m, _ = node.(map[string]any)
		}
		return m
	}
	out := map[string]bool{}
	for path, item := range doc["paths"].(map[string]any) {
		pi := item.(map[string]any)
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			op, ok := pi[method].(map[string]any)
			if !ok {
				continue
			}
			params, _ := op["parameters"].([]any)
			for _, p := range params {
				if pm := resolve(p); pm != nil && pm["in"] == "query" && pm["name"] == "dry_run" {
					out[strings.ToUpper(method)+" "+path] = true
				}
			}
		}
	}
	return out
}

func TestNoLocalDryRunAndServerDryRunIsBound(t *testing.T) {
	ops := specDryRunOperations(t)
	if len(ops) < 100 {
		t.Fatalf("spec.yaml shows only %d operations with a dry_run query parameter; the scan is blind", len(ops))
	}
	root := newRootCmd(&rootFlags{})
	if root.PersistentFlags().Lookup("dry-run") == nil {
		t.Fatal("the global --dry-run is missing")
	}
	seen := map[string]bool{}
	var walk func(c *cobra.Command, chain []string)
	walk = func(c *cobra.Command, chain []string) {
		if c != root && c.LocalNonPersistentFlags().Lookup("dry-run") != nil {
			t.Errorf("%s registers a local --dry-run, which would shadow the global preview", c.CommandPath())
		}
		key := c.Annotations["pp:method"] + " " + c.Annotations["pp:path"]
		if ops[key] && !serverDryRunExempt[key] {
			seen[key] = true
			if f := c.Flags().Lookup("server-dry-run"); f == nil || !strings.Contains(f.Usage, "the request IS sent") {
				t.Errorf("%s (%s): --server-dry-run is %v", c.CommandPath(), key, f)
			} else {
				dryRunPreviewSendsNothing(t, c, chain)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub, append(append([]string{}, chain...), sub.Name()))
		}
	}
	walk(root, nil)
	for op := range ops {
		if !seen[op] && !serverDryRunExempt[op] {
			t.Errorf("%s has a dry_run query parameter but no command binds --server-dry-run", op)
		}
	}
}

// dryRunPreviewSendsNothing runs the command with the global --dry-run (and --server-dry-run) against a server that counts
// requests; the preview must print and send nothing.
func dryRunPreviewSendsNothing(t *testing.T, c *cobra.Command, chain []string) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer srv.Close()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL+"/api/v1")
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("CONDUYT_CRM_NO_LEARN", "true")
	t.Setenv("HOME", t.TempDir())
	root := newRootCmd(&rootFlags{})
	args := append(append([]string{}, chain...), "--dry-run", "--server-dry-run", "--no-cache")
	for i := 0; i < strings.Count(c.Use, "<"); i++ {
		args = append(args, "00000000-0000-4000-8000-00000000000"+string(rune('1'+i)))
	}
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("%s --dry-run sent %d request(s) (err %v), want none", c.CommandPath(), n, err)
	}
}
