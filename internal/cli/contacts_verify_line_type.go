// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
// Novel command: contacts verify-line-type — cost estimate + batched
// loop-until-done driver for the opt-in Twilio line-type verification.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ptaramona/conduyt-crm-cli/internal/store"
	"github.com/spf13/cobra"
)

// pp:data-source auto
func newNovelContactsVerifyLineTypeCmd(flags *rootFlags) *cobra.Command {
	var flagSmartList string
	var flagContactIDs []string
	var flagEstimate bool
	var flagMaxBatches int
	var dbPath string

	cmd := &cobra.Command{
		Use:   "verify-line-type",
		Short: "Estimate, then drive Twilio line-type verification to done (tenant-paid)",
		Long:  "Drives the batched, idempotent line-type verification loop (~$0.008 per number, billed to the tenant's own Twilio) until done. Requires the account's Phone Line-Type Verification setting and org-wide contact access. With --estimate, no money is spent: the local store is counted for unverified contacts (tenant-wide upper bound when scoping to a smart list) and the cost exposure is printed.",
		Example: strings.Trim(`
  conduyt-crm-pp-cli contacts verify-line-type --estimate
  conduyt-crm-pp-cli contacts verify-line-type --smart-list 7c1d2e3f-0000-0000-0000-000000000000
  conduyt-crm-pp-cli contacts verify-line-type --contact-ids 9e778307-e575-420b-b02c-dc9cc564febd --json`, "\n"),
		Annotations: map[string]string{"pp:typed-exit-codes": "0,2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagEstimate {
				if dbPath == "" {
					dbPath = defaultDBPath("conduyt-crm-pp-cli")
				}
				db, err := store.OpenWithContext(cmd.Context(), dbPath)
				if err != nil {
					return fmt.Errorf("opening local store (run 'sync' first for estimates): %w", err)
				}
				defer db.Close()
				var unverified int
				row := db.DB().QueryRowContext(cmd.Context(), `
					SELECT COUNT(*) FROM resources
					WHERE resource_type = 'contacts'
					  AND COALESCE(json_extract(data, '$.phone'), '') <> ''
					  AND json_extract(data, '$.customFields.sms_line_type') IS NULL`)
				if err := row.Scan(&unverified); err != nil {
					return fmt.Errorf("counting unverified contacts: %w", err)
				}
				est := map[string]any{
					"unverifiedContacts": unverified,
					"batchesOf50":        (unverified + 49) / 50,
					"estimatedCostUSD":   fmt.Sprintf("%.2f", float64(unverified)*0.008),
					"note":               "Tenant-wide count from the local store (sync first for accuracy). A smart-list scope verifies at most this many.",
				}
				return printJSONFiltered(cmd.OutOrStdout(), est, flags)
			}
			if flagSmartList == "" && len(flagContactIDs) == 0 {
				return cmd.Help()
			}
			if dryRunOK(flags) {
				return nil
			}
			c, err := flags.newClient()
			if err != nil {
				return err
			}
			body := map[string]any{}
			if flagSmartList != "" {
				body["smartListId"] = flagSmartList
			} else {
				body["contactIds"] = flagContactIDs
			}
			totals := map[string]float64{}
			verified, batches := 0, 0
			for batches = 0; batches < flagMaxBatches; batches++ {
				data, _, err := c.Post(cmd.Context(), "/contacts/verify-line-type", body)
				if err != nil {
					return classifyAPIError(err, flags)
				}
				var envelope map[string]any
				if err := json.Unmarshal(data, &envelope); err != nil {
					return fmt.Errorf("parsing response: %w", err)
				}
				d, ok := envelope["data"].(map[string]any)
				if !ok {
					d = envelope
				}
				if v, ok := d["verified"].(float64); ok {
					verified += int(v)
				}
				if bl, ok := d["byLineType"].(map[string]any); ok {
					for k, v := range bl {
						if n, ok := v.(float64); ok {
							totals[k] += n
						}
					}
				}
				done, _ := d["done"].(bool)
				remaining, _ := d["remaining"].(float64)
				fmt.Fprintf(cmd.ErrOrStderr(), "batch %d: verified=%d remaining=%d\n", batches+1, verified, int(remaining))
				if done {
					out := map[string]any{"verified": verified, "batches": batches + 1, "done": true, "byLineType": totals}
					return printJSONFiltered(cmd.OutOrStdout(), out, flags)
				}
			}
			out := map[string]any{"verified": verified, "batches": batches, "done": false, "byLineType": totals}
			if err := printJSONFiltered(cmd.OutOrStdout(), out, flags); err != nil {
				return err
			}
			return &cliError{code: 2, err: fmt.Errorf("stopped at --max-batches %d before done", flagMaxBatches)}
		},
	}
	cmd.Flags().StringVar(&flagSmartList, "smart-list", "", "Smart list UUID whose unstamped members to verify")
	cmd.Flags().StringSliceVar(&flagContactIDs, "contact-ids", nil, "Explicit contact UUIDs to verify (max 50,000)")
	cmd.Flags().BoolVar(&flagEstimate, "estimate", false, "Spend nothing: count unverified contacts locally and print the Twilio cost exposure")
	cmd.Flags().IntVar(&flagMaxBatches, "max-batches", 400, "Safety cap on paid batches per run (50 lookups each)")
	cmd.Flags().StringVar(&dbPath, "db", "", "Local store path for --estimate (defaults to the CLI's store)")
	return cmd
}
