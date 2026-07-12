// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
// Novel command: drips audit — at-most-once reconciliation for a drip cohort,
// computed over synced MESSAGES in the local store (the user-visible truth).
package cli

import (
	"fmt"
	"strings"

	"github.com/ptaramona/conduyt-crm-cli/internal/store"
	"github.com/spf13/cobra"
)

// pp:data-source local
func newNovelDripsAuditCmd(flags *rootFlags) *cobra.Command {
	var flagSince string
	var dbPath string

	cmd := &cobra.Command{
		Use:   "audit [campaignId]",
		Short: "Flag double-sends in a drip cohort (at-most-once check over synced messages)",
		Long:  "Reconciles outbound SMS in the local store to enforce the at-most-once protocol: any contact who received the SAME message body more than once in the window is flagged with counts. Runs entirely offline over synced messages — run 'sync' first. The campaignId argument filters to messages mentioning that campaign when the message rows carry it; omit to audit all outbound SMS in the window.",
		Example: strings.Trim(`
  conduyt-crm-pp-cli drips audit --since 2026-07-01
  conduyt-crm-pp-cli drips audit 9b8c7d6e-0000-0000-0000-000000000000 --json`, "\n"),
		Annotations: map[string]string{"mcp:read-only": "true", "pp:typed-exit-codes": "0,2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRunOK(flags) {
				return nil
			}
			if dbPath == "" {
				dbPath = defaultDBPath("conduyt-crm-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening local store (run 'sync' first): %w", err)
			}
			defer db.Close()

			where := `resource_type = 'messages'
				AND json_extract(data, '$.direction') = 'outbound'
				AND json_extract(data, '$.channel') = 'sms'`
			params := []any{}
			if flagSince != "" {
				where += ` AND json_extract(data, '$.createdAt') >= ?`
				params = append(params, flagSince)
			}
			if len(args) > 0 {
				where += ` AND json_extract(data, '$.campaignId') = ?`
				params = append(params, args[0])
			}
			rows, err := db.DB().QueryContext(cmd.Context(), `
				SELECT json_extract(data, '$.contactId') AS contact_id,
				       substr(COALESCE(json_extract(data, '$.body'),''), 1, 80) AS body_head,
				       COUNT(*) AS n
				FROM resources
				WHERE `+where+`
				GROUP BY contact_id, body_head
				HAVING COUNT(*) > 1
				ORDER BY n DESC
				LIMIT 500`, params...)
			if err != nil {
				return fmt.Errorf("audit query: %w", err)
			}
			defer rows.Close()
			type dup struct {
				ContactID string `json:"contactId"`
				BodyHead  string `json:"bodyHead"`
				Count     int    `json:"count"`
			}
			dups := []dup{}
			for rows.Next() {
				var d dup
				if err := rows.Scan(&d.ContactID, &d.BodyHead, &d.Count); err != nil {
					return err
				}
				dups = append(dups, d)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			var total int
			if err := db.DB().QueryRowContext(cmd.Context(), `SELECT COUNT(*) FROM resources WHERE `+where, params...).Scan(&total); err != nil {
				return err
			}
			out := map[string]any{
				"summary":     map[string]any{"outboundSmsChecked": total, "doubleSendGroups": len(dups)},
				"doubleSends": dups,
			}
			if err := printJSONFiltered(cmd.OutOrStdout(), out, flags); err != nil {
				return err
			}
			if len(dups) > 0 {
				return &cliError{code: 2, err: fmt.Errorf("%d double-send group(s) found", len(dups))}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&flagSince, "since", "", "Only audit messages created on/after this ISO date")
	cmd.Flags().StringVar(&dbPath, "db", "", "Local store path (defaults to the CLI's store)")
	return cmd
}
