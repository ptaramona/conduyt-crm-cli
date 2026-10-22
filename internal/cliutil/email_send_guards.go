package cliutil

import (
	"fmt"
	"sort"
	"strings"
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

// CheckEmailSendFlags checks what the live spec requires of the flag-built POST /email/send body, so a body the CLI builds
// is one the spec accepts: to (contains "@"), a non-blank subject, a contact UUID, and at least one of html or text.
func CheckEmailSendFlags(to, subject, contactID, html, text string) error {
	if !strings.Contains(to, "@") {
		return fmt.Errorf("--to must be an email address (it must contain \"@\")")
	}
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("--subject must contain at least one non-whitespace character")
	}
	if err := CheckUUID("--contact-id", contactID); err != nil {
		return err
	}
	if html == "" && text == "" {
		return fmt.Errorf("give --html, --text or both: the send needs at least one body")
	}
	return nil
}
