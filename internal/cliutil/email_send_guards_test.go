package cliutil

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// specBodyKeys reads the request-body property names of one operation straight from upstream/openapi.yaml.
func specBodyKeys(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "upstream", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Properties map[string]any `yaml:"properties"`
					} `yaml:"schema"`
				} `yaml:"content"`
			} `yaml:"requestBody"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc.Paths[path]["post"].RequestBody.Content["application/json"].Schema.Properties
	if len(props) == 0 {
		t.Fatalf("no request body properties for POST %s in upstream/openapi.yaml", path)
	}
	var keys []string
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// The key lists the pass-through filter accepts are exactly the properties the public spec declares. A key the spec accepts is
// never refused, and a key it dropped is never let through.
func TestEmailSendKeysMatchPublicSpec(t *testing.T) {
	for path, got := range map[string][]string{"/email/send": EmailSendKeys, "/email/send-bulk": EmailSendBulkKeys} {
		sorted := append([]string{}, got...)
		sort.Strings(sorted)
		if want := specBodyKeys(t, path); !reflect.DeepEqual(sorted, want) {
			t.Errorf("%s: filter accepts %v, spec declares %v", path, sorted, want)
		}
	}
}

func TestCheckEmailSendBodyKeys(t *testing.T) {
	if err := CheckEmailSendBodyKeys("email.send", map[string]any{"to": "a@b", "html": nil, "cc": []any{}}); err != nil {
		t.Errorf("known keys refused: %v", err)
	}
	err := CheckEmailSendBodyKeys("email.send", map[string]any{"to": "a@b", "body": "x", "bodyHtml": "y"})
	if err == nil || !strings.Contains(err.Error(), `"body", "bodyHtml"`) || !strings.Contains(err.Error(), "accepted keys: bcc, cc, contactId") {
		t.Errorf("unknown keys: %v", err)
	}
	if err := CheckEmailSendBodyKeys("email.send-bulk", map[string]any{"sandbox": true}); err == nil {
		t.Errorf("a query name must be refused unless the caller passes it as extra")
	}
	if err := CheckEmailSendBodyKeys("email.send-bulk", map[string]any{"sandbox": true}, "sandbox"); err != nil {
		t.Errorf("extra key refused: %v", err)
	}
	if err := CheckEmailSendBodyKeys("email.other", nil); err == nil {
		t.Errorf("unknown endpoint id must not pass silently")
	}
}
