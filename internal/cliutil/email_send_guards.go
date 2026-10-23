package cliutil

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

// Hand-written (Go CLI #324): the request-body keys POST /email/send and POST /email/send-bulk accept in the live public
// OpenAPI (https://conduyt.app/openapi.yaml, requestBody properties). The generator cannot print this pair: /email/send is an
// anyOf body (html or text), which it renders as a single --body-json flag, and the keys it does print would be the old ones.
// TestEmailSendKeysMatchPublicSpec pins these lists to upstream/openapi.yaml, so a spec refresh that adds or drops a key fails
// there instead of silently refusing or passing a key. A reprint must keep these guards and the two commands' hand-written bodies.
//
// The pass-through modes (--stdin on the CLI, params on the MCP execute tool) used to send any key straight to the API. A key
// outside the lists is refused locally with the accepted keys, never dropped and never sent: the API validates the body as a
// strict object, and a key it does not know is a 400 there anyway.
var (
	// EmailSendKeys are the body properties of POST /email/send.
	EmailSendKeys = []string{"to", "subject", "html", "text", "contactId", "replyTo", "cc", "bcc", "sendDomainId"}
	// EmailSendBulkKeys are the body properties of POST /email/send-bulk.
	EmailSendBulkKeys = []string{"contactIds", "subject", "text", "html", "templateId", "subjectEdited", "batchDelayMs", "replyTo", "sendDomainId"}
)

// CheckEmailSendBodyKeys refuses any key of body that the endpoint's request body does not declare. endpoint is the
// code-orchestration id ("email.send" or "email.send-bulk"). extra names keys that are legitimate for the caller but ride
// outside the body (the MCP executor passes the spec-declared query parameters it has not split off yet).
func CheckEmailSendBodyKeys(endpoint string, body map[string]any, extra ...string) error {
	var accepted []string
	switch endpoint {
	case "email.send":
		accepted = EmailSendKeys
	case "email.send-bulk":
		accepted = EmailSendBulkKeys
	default:
		return fmt.Errorf("no key list for endpoint %q", endpoint)
	}
	ok := map[string]bool{}
	for _, k := range accepted {
		ok[k] = true
	}
	for _, k := range extra {
		ok[k] = true
	}
	var unknown []string
	for k := range body {
		if !ok[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	list := append([]string{}, accepted...)
	sort.Strings(list)
	return fmt.Errorf("%s does not accept %s; accepted keys: %s (nothing was sent)",
		strings.Replace(endpoint, ".", " ", 1), strings.Join(quoteAll(unknown), ", "), strings.Join(list, ", "))
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// The request-body limits of POST /email/send in the live spec. Constants, not parsed at run time, so the binary needs no spec file;
// TestEmailSendLimitsMatchPublicSpec reads maxLength and pattern for each from upstream/openapi.yaml and fails on any difference,
// so a spec refresh that moves a limit cannot go unnoticed. Lengths are UTF-16 code units, as the spec states.
const (
	EmailSendToMax        = 254
	EmailSendSubjectMax   = 998
	EmailSendHTMLMax      = 500000
	EmailSendTextMax      = 100000
	EmailSendToPattern    = `@`
	EmailSendSubjectRE    = `\S`
	EmailSendContactIDRE  = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	EmailSendReplyToRE    = `^\s*(\S(?:.{0,252}\S)?)?\s*$`
	EmailSendSendDomainRE = `^\s*([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})?\s*$`
)

// jsSpace is the character set of ECMAScript \s. The spec's patterns are JavaScript regular expressions; Go's \s is ASCII only, so
// a value the spec accepts (padding with a no-break space) must not be refused here.
const jsSpace = "\\t\\n\\v\\f\\r \\x{00a0}\\x{1680}\\x{2000}-\\x{200a}\\x{2028}\\x{2029}\\x{202f}\\x{205f}\\x{3000}\\x{feff}"

func compileJSPattern(p string) *regexp.Regexp {
	p = strings.ReplaceAll(p, `\S`, "[^"+jsSpace+"]")
	p = strings.ReplaceAll(p, `\s`, "["+jsSpace+"]")
	return regexp.MustCompile(p)
}

var (
	emailSendToRE      = compileJSPattern(EmailSendToPattern)
	emailSendSubjectRE = compileJSPattern(EmailSendSubjectRE)
	emailSendContactRE = compileJSPattern(EmailSendContactIDRE)
	emailSendReplyToRE = compileJSPattern(EmailSendReplyToRE)
	emailSendSendDomRE = compileJSPattern(EmailSendSendDomainRE)
)

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// EmailSendFlagBody is what the flags of `email send` would put in the body. HasReplyTo and HasSendDomainID say whether the flag
// was given (a given empty value is sent and is valid; an absent one is not sent).
type EmailSendFlagBody struct {
	To, Subject, ContactID, HTML, Text, ReplyTo, SendDomainID string
	HasReplyTo, HasSendDomainID                               bool
}

// CheckEmailSendFlags checks the flag-built POST /email/send body against the live spec, so a body the CLI builds is one the spec
// accepts: required to, subject, contactId and one of html/text; maxLength and pattern of every field; no unsent junk. It runs for
// --dry-run too, so a dry run previews only a body a real send would post.
func CheckEmailSendFlags(b EmailSendFlagBody) error {
	if !emailSendToRE.MatchString(b.To) {
		return fmt.Errorf("--to must be an email address (it must contain \"@\")")
	}
	if n := utf16Len(b.To); n > EmailSendToMax {
		return fmt.Errorf("--to is %d characters; the maximum is %d", n, EmailSendToMax)
	}
	if !emailSendSubjectRE.MatchString(b.Subject) {
		return fmt.Errorf("--subject must contain at least one non-whitespace character")
	}
	if n := utf16Len(b.Subject); n > EmailSendSubjectMax {
		return fmt.Errorf("--subject is %d characters; the maximum is %d", n, EmailSendSubjectMax)
	}
	if !emailSendContactRE.MatchString(b.ContactID) {
		return fmt.Errorf("--contact-id must be a UUID (got %q)", b.ContactID)
	}
	if b.HTML == "" && b.Text == "" {
		return fmt.Errorf("give --html, --text or both: the send needs at least one body")
	}
	if n := utf16Len(b.HTML); n > EmailSendHTMLMax {
		return fmt.Errorf("--html is %d characters; the maximum is %d", n, EmailSendHTMLMax)
	}
	if n := utf16Len(b.Text); n > EmailSendTextMax {
		return fmt.Errorf("--text is %d characters; the maximum is %d", n, EmailSendTextMax)
	}
	if b.HasReplyTo && !emailSendReplyToRE.MatchString(b.ReplyTo) {
		return fmt.Errorf("--reply-to must be an email address of at most 254 characters after trimming (got %q)", b.ReplyTo)
	}
	if b.HasSendDomainID && !emailSendSendDomRE.MatchString(b.SendDomainID) {
		return fmt.Errorf("--send-domain-id must be a UUID or blank (got %q)", b.SendDomainID)
	}
	return nil
}
