// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
// Novel command: send-check — the anti-Kloudi gate. Go/no-go verdict on a
// segment before any SMS/drip: phone present, verified line type, DNC status.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// pp:data-source live
func newNovelSendCheckCmd(flags *rootFlags) *cobra.Command {
	var flagTag []string
	var flagList string
	var flagContact string
	var flagLimit int

	cmd := &cobra.Command{
		Use:   "send-check",
		Short: "Go/no-go verdict on a list, tag, or contact before any SMS send",
		Long:  "The anti-Kloudi gate: audits a segment BEFORE messaging it. Per contact: phone present? Twilio-verified line type (landline = blocked)? Twilio validity? Account-wide DNC/litigator match? Returns a verdict table plus a summary; exit 0 when clean, exit 2 when anything is blocked — safe to gate a pipeline on. DNC checks need the explicit 'dnc' key scope; without it the check degrades with a warning.",
		Example: strings.Trim(`
  conduyt-crm-pp-cli send-check --tag new-leads
  conduyt-crm-pp-cli send-check --contact 9e778307-e575-420b-b02c-dc9cc564febd --json
  conduyt-crm-pp-cli send-check --list 7c1d2e3f-0000-0000-0000-000000000000 --agent --select summary`, "\n"),
		Annotations: map[string]string{"mcp:read-only": "true", "pp:typed-exit-codes": "0,2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagContact == "" && flagList == "" && len(flagTag) == 0 {
				return cmd.Help()
			}
			if dryRunOK(flags) {
				return nil
			}
			c, err := flags.newClient()
			if err != nil {
				return err
			}

			// 1) Collect the segment's contacts.
			type contactRow struct {
				ID, Name, Phone, LineType string
				Valid                     any
			}
			var rows []contactRow
			addContact := func(m map[string]any) {
				id, _ := m["id"].(string)
				phone, _ := m["phone"].(string)
				first, _ := m["firstName"].(string)
				last, _ := m["lastName"].(string)
				lineType, valid := "", any(nil)
				if cf, ok := m["customFields"].(map[string]any); ok {
					if lt, ok := cf["sms_line_type"].(string); ok {
						lineType = lt
					}
					valid = cf["sms_phone_valid"]
				}
				rows = append(rows, contactRow{ID: id, Name: strings.TrimSpace(first + " " + last), Phone: phone, LineType: lineType, Valid: valid})
			}
			collect := func(data json.RawMessage) {
				var env map[string]any
				if json.Unmarshal(data, &env) != nil {
					return
				}
				d, _ := env["data"].(map[string]any)
				if d == nil {
					if m, ok := env["data"].([]any); ok {
						for _, it := range m {
							if mm, ok := it.(map[string]any); ok {
								addContact(mm)
							}
						}
					}
					return
				}
				for _, key := range []string{"data", "contacts", "items"} {
					if items, ok := d[key].([]any); ok {
						for _, it := range items {
							if mm, ok := it.(map[string]any); ok {
								addContact(mm)
							}
						}
						return
					}
				}
				if _, hasID := d["id"]; hasID {
					addContact(d)
				}
			}
			switch {
			case flagContact != "":
				data, err := c.Get(cmd.Context(), "/contacts/"+flagContact, nil)
				if err != nil {
					return classifyAPIError(err, flags)
				}
				collect(data)
			case flagList != "":
				data, err := c.Get(cmd.Context(), "/smart-lists/"+flagList+"/contacts", map[string]string{"per_page": fmt.Sprint(flagLimit)})
				if err != nil {
					return classifyAPIError(err, flags)
				}
				collect(data)
			default:
				for _, tag := range flagTag {
					data, err := c.Get(cmd.Context(), "/contacts", map[string]string{"tag": tag, "per_page": fmt.Sprint(flagLimit)})
					if err != nil {
						return classifyAPIError(err, flags)
					}
					collect(data)
				}
			}
			if len(rows) == 0 {
				return notFoundErr(fmt.Errorf("no contacts found in the given scope"))
			}

			// 2) DNC list (one paged read, matched locally on digit suffix).
			dncSuffixes := map[string]bool{}
			dncAvailable := true
			if data, err := c.Get(cmd.Context(), "/dnc", map[string]string{"per_page": "200"}); err == nil {
				var env map[string]any
				if json.Unmarshal(data, &env) == nil {
					var items []any
					if d, ok := env["data"].(map[string]any); ok {
						if it, ok := d["data"].([]any); ok {
							items = it
						} else if it, ok := d["entries"].([]any); ok {
							items = it
						}
					} else if it, ok := env["data"].([]any); ok {
						items = it
					}
					for _, it := range items {
						if m, ok := it.(map[string]any); ok {
							p, _ := m["normalizedPhone"].(string)
							if p == "" {
								p, _ = m["phone"].(string)
							}
							if d := digitsOnlySuffix(p, 7); d != "" {
								dncSuffixes[d] = true
							}
						}
					}
				}
			} else {
				dncAvailable = false
			}

			// 3) Verdicts.
			type verdict struct {
				ContactID string `json:"contactId"`
				Name      string `json:"name,omitempty"`
				Phone     string `json:"phone,omitempty"`
				LineType  string `json:"lineType,omitempty"`
				Status    string `json:"status"`
				Reason    string `json:"reason,omitempty"`
			}
			var verdicts []verdict
			blocked, warned := 0, 0
			for _, r := range rows {
				v := verdict{ContactID: r.ID, Name: r.Name, Phone: r.Phone, LineType: r.LineType}
				switch {
				case strings.TrimSpace(r.Phone) == "":
					v.Status, v.Reason = "blocked", "no phone number"
				case dncAvailable && dncSuffixes[digitsOnlySuffix(r.Phone, 7)]:
					v.Status, v.Reason = "blocked", "on the DNC/litigator list"
				case r.LineType == "landline":
					v.Status, v.Reason = "blocked", "Twilio-verified landline"
				case r.Valid == false:
					v.Status, v.Reason = "blocked", "Twilio says the number is invalid"
				case r.LineType == "":
					v.Status, v.Reason = "warn", "line type not verified yet"
				default:
					v.Status = "ok"
				}
				switch v.Status {
				case "blocked":
					blocked++
				case "warn":
					warned++
				}
				verdicts = append(verdicts, v)
			}
			out := map[string]any{
				"summary": map[string]any{
					"checked": len(rows), "ok": len(rows) - blocked - warned,
					"warn": warned, "blocked": blocked, "dncChecked": dncAvailable,
				},
				"verdicts": verdicts,
			}
			if !dncAvailable {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: DNC list unavailable (needs the explicit 'dnc' key scope) — DNC status NOT checked")
			}
			if err := printJSONFiltered(cmd.OutOrStdout(), out, flags); err != nil {
				return err
			}
			if blocked > 0 {
				return &cliError{code: 2, err: fmt.Errorf("%d of %d contacts blocked", blocked, len(rows))}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&flagTag, "tag", nil, "Check every contact carrying this tag (repeatable)")
	cmd.Flags().StringVar(&flagList, "list", "", "Check the members of this smart list (UUID)")
	cmd.Flags().StringVar(&flagContact, "contact", "", "Check a single contact (UUID)")
	cmd.Flags().IntVar(&flagLimit, "limit", 200, "Max contacts to check per scope")
	return cmd
}

// digitsOnlySuffix returns the trailing n digits of a phone-ish string.
func digitsOnlySuffix(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 0 {
		return ""
	}
	if len(d) > n {
		return d[len(d)-n:]
	}
	return d
}
