package cli

// Codex reprint637-r2. PATCH(reprint-637-required-zero-values), PATCH(reprint-637-conditional-requirements).

import (
	"encoding/json"
	"testing"
)

func bodyOf(t *testing.T, r recordedRequest) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		t.Fatalf("body %q: %v", r.Body, err)
	}
	return m
}

// A required numeric body field passed as an explicit 0 is sent (0 is a valid value: no folders, Sunday, midnight).
func TestRequiredZeroValuesAreSent(t *testing.T) {
	for _, tc := range []struct {
		args []string
		key  string
	}{
		{[]string{"chat", "put-agents-me-knowledge", "--configured=false", "--folders=0", "--listener-id", "L1"}, "folders"},
		{[]string{"rep-shifts", "post", "--user-id", "u1", "--day-of-week", "0", "--start-minute", "0", "--end-minute", "60"}, "dayOfWeek"},
		{[]string{"rep-shifts", "post", "--user-id", "u1", "--day-of-week", "1", "--start-minute", "0", "--end-minute", "60"}, "startMinute"},
		{[]string{"notifications", "update-digest-settings", "--enabled=true", "--frequency", "daily", "--hour", "0"}, "hour"},
	} {
		srv, reqs := recordingServer(t)
		if err := runCLI(t, srv.URL, append(tc.args, "--yes")...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		got := reqs()
		if len(got) != 1 {
			t.Fatalf("%v sent %d requests", tc.args, len(got))
		}
		body := bodyOf(t, got[0])
		if v, ok := body[tc.key]; !ok || v != float64(0) {
			t.Fatalf("%v: body %s, want %s: 0", tc.args, got[0].Body, tc.key)
		}
	}
}
