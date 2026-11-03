package cli

// Contract pin for POST /dialer/call (operationId initiateCall, board #930): the body keys the generated command sends, and the
// params the MCP mirror declares, must be exactly the properties of the request schema in upstream/openapi.yaml. A reprint or hand
// edit that renames a key (the live API reads `to`, not `toNumber`) fails here.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func initiateCallSchema(t *testing.T) (props []string, required []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "upstream", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Required   []string       `yaml:"required"`
						Properties map[string]any `yaml:"properties"`
					} `yaml:"schema"`
				} `yaml:"content"`
			} `yaml:"requestBody"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	op, ok := doc.Paths["/dialer/call"]["post"]
	if !ok || op.OperationID != "initiateCall" {
		t.Fatalf("upstream/openapi.yaml has no initiateCall at POST /dialer/call (got %+v)", op.OperationID)
	}
	s := op.RequestBody.Content["application/json"].Schema
	for k := range s.Properties {
		props = append(props, k)
	}
	sort.Strings(props)
	if len(props) == 0 {
		t.Fatal("initiateCall request schema has no properties")
	}
	return props, s.Required
}

func TestInitiateCallBodyKeysMatchUpstreamSchema(t *testing.T) {
	props, required := initiateCallSchema(t)
	if !reflect.DeepEqual(required, []string{"to"}) {
		t.Errorf("upstream required = %v, this pin expects [to]; review the command's required-flag check", required)
	}

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/dialer/call" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("body is not JSON: %q", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	t.Setenv("CONDUYT_CRM_BASE_URL", srv.URL+"/api/v1")
	t.Setenv("CONDUYT_CRM_BEARER_AUTH", "test-token")
	t.Setenv("CONDUYT_CRM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))

	// Every body flag set, so every key the command can emit appears in the body.
	cmd := newDialerInitiateCallCmd(&rootFlags{noCache: true})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--to", "+15555550100",
		"--contact-id", "00000000-0000-0000-0000-000000000000",
		"--dnc-override", "--auto-dialer",
		"--caller-id-strategy", "did",
		"--vendor-bridge-nonce", "n",
		"--vendor-bridgeable",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, props) {
		t.Errorf("command body keys %v != upstream initiateCall properties %v", keys, props)
	}
}

// The MCP mirror: tools-manifest.json declares the body params of dialer_initiate-call, and the code-orchestration entry must
// still route dialer.initiate-call to POST /dialer/call (its params pass through as the body, so the manifest is the key list).
func TestInitiateCallMCPMirrorMatchesUpstreamSchema(t *testing.T) {
	props, required := initiateCallSchema(t)
	raw, err := readRepoFile("tools-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tools []struct {
			Name   string `json:"name"`
			Method string `json:"method"`
			Path   string `json:"path"`
			Params []struct {
				Name     string `json:"name"`
				Location string `json:"location"`
				Required bool   `json:"required"`
			} `json:"params"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range m.Tools {
		if tl.Name != "dialer_initiate-call" {
			continue
		}
		found = true
		if tl.Method != "POST" || tl.Path != "/dialer/call" {
			t.Errorf("manifest route = %s %s", tl.Method, tl.Path)
		}
		var keys, req []string
		for _, p := range tl.Params {
			if p.Location != "body" {
				t.Errorf("param %s location %q, want body", p.Name, p.Location)
			}
			keys = append(keys, p.Name)
			if p.Required {
				req = append(req, p.Name)
			}
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, props) {
			t.Errorf("tools-manifest dialer_initiate-call params %v != upstream properties %v", keys, props)
		}
		if !reflect.DeepEqual(req, required) {
			t.Errorf("tools-manifest required %v != upstream required %v", req, required)
		}
	}
	if !found {
		t.Fatal("tools-manifest.json has no dialer_initiate-call")
	}
	src, err := readRepoFile("internal/mcp/code_orch.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(src, `ID:             "dialer.initiate-call"`)
	if i < 0 || !strings.Contains(src[i:i+200], `Path:           "/dialer/call"`) || !strings.Contains(src[i:i+200], `Method:         "POST"`) {
		t.Error("internal/mcp/code_orch.go dialer.initiate-call no longer routes to POST /dialer/call")
	}
}
