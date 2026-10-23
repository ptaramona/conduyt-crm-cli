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

// specSendProps returns the property schemas of POST /email/send from upstream/openapi.yaml.
func specSendProps(t *testing.T) map[string]struct {
	MaxLength int    `yaml:"maxLength"`
	Pattern   string `yaml:"pattern"`
} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "upstream", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	type prop = struct {
		MaxLength int    `yaml:"maxLength"`
		Pattern   string `yaml:"pattern"`
	}
	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Properties map[string]prop `yaml:"properties"`
					} `yaml:"schema"`
				} `yaml:"content"`
			} `yaml:"requestBody"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Paths["/email/send"]["post"].RequestBody.Content["application/json"].Schema.Properties
}

// Every limit CheckEmailSendFlags enforces is the one the live spec declares, so a refresh that moves a limit fails here.
func TestEmailSendLimitsMatchPublicSpec(t *testing.T) {
	p := specSendProps(t)
	for name, c := range map[string]struct {
		prop    string
		max     int
		pattern string
	}{
		"to":           {"to", EmailSendToMax, EmailSendToPattern},
		"subject":      {"subject", EmailSendSubjectMax, EmailSendSubjectRE},
		"html":         {"html", EmailSendHTMLMax, ""},
		"text":         {"text", EmailSendTextMax, ""},
		"contactId":    {"contactId", 0, EmailSendContactIDRE},
		"replyTo":      {"replyTo", 0, EmailSendReplyToRE},
		"sendDomainId": {"sendDomainId", 0, EmailSendSendDomainRE},
	} {
		got := p[c.prop]
		if got.MaxLength != c.max || got.Pattern != c.pattern {
			t.Errorf("%s: code has maxLength=%d pattern=%q, spec has maxLength=%d pattern=%q", name, c.max, c.pattern, got.MaxLength, got.Pattern)
		}
	}
}

func okSendBody() EmailSendFlagBody {
	return EmailSendFlagBody{To: "a@b.co", Subject: "S", ContactID: "6f1c0000-0000-4000-8000-0000000000a1", HTML: "<p>x</p>"}
}

// One test per limit: the value at the limit passes, one past it is refused; characters outside the BMP count as two.
func TestCheckEmailSendFlagsLimits(t *testing.T) {
	at := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name   string
		set    func(*EmailSendFlagBody, string)
		okVal  string
		badVal string
	}{
		{"to max", func(b *EmailSendFlagBody, v string) { b.To = v }, at(EmailSendToMax-2) + "@b", at(EmailSendToMax-1) + "@b"},
		{"subject max", func(b *EmailSendFlagBody, v string) { b.Subject = v }, at(EmailSendSubjectMax), at(EmailSendSubjectMax + 1)},
		{"html max", func(b *EmailSendFlagBody, v string) { b.HTML = v }, at(EmailSendHTMLMax), at(EmailSendHTMLMax + 1)},
		{"text max", func(b *EmailSendFlagBody, v string) { b.Text = v }, at(EmailSendTextMax), at(EmailSendTextMax + 1)},
		{"subject astral counts twice", func(b *EmailSendFlagBody, v string) { b.Subject = v }, strings.Repeat("\U0001F600", EmailSendSubjectMax/2), strings.Repeat("\U0001F600", EmailSendSubjectMax/2+1)},
		{"to needs @", func(b *EmailSendFlagBody, v string) { b.To = v }, "a@b", "ab"},
		{"subject non-blank", func(b *EmailSendFlagBody, v string) { b.Subject = v }, "x", " \t\n"},
		{"contact uuid", func(b *EmailSendFlagBody, v string) { b.ContactID = v }, "6f1c0000-0000-4000-8000-0000000000a1", " 6f1c0000-0000-4000-8000-0000000000a1"},
		{"reply-to max after trim", func(b *EmailSendFlagBody, v string) { b.ReplyTo, b.HasReplyTo = v, true }, "  " + at(254) + "  ", at(255)},
		{"send-domain uuid", func(b *EmailSendFlagBody, v string) { b.SendDomainID, b.HasSendDomainID = v, true }, " 6f1c0000-0000-4000-8000-0000000000a4 ", "not-a-uuid"},
	}
	for _, c := range cases {
		ok := okSendBody()
		c.set(&ok, c.okVal)
		if err := CheckEmailSendFlags(ok); err != nil {
			t.Errorf("%s: value at the limit refused: %v", c.name, err)
		}
		bad := okSendBody()
		c.set(&bad, c.badVal)
		if err := CheckEmailSendFlags(bad); err == nil {
			t.Errorf("%s: value past the limit accepted", c.name)
		}
	}
	// blank reply-to and send-domain are valid per the spec patterns when given; absent ones are not checked
	b := okSendBody()
	b.ReplyTo, b.HasReplyTo, b.SendDomainID, b.HasSendDomainID = "", true, "", true
	if err := CheckEmailSendFlags(b); err != nil {
		t.Errorf("blank optional values: %v", err)
	}
	// a no-break space is JavaScript whitespace: the spec accepts it around a reply-to
	b.ReplyTo = "\u00a0a@b.co\u00a0"
	if err := CheckEmailSendFlags(b); err != nil {
		t.Errorf("no-break space padding: %v", err)
	}
	b = okSendBody()
	b.HTML, b.Text = "", ""
	if err := CheckEmailSendFlags(b); err == nil {
		t.Errorf("no body accepted")
	}
}
