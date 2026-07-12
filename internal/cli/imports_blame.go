// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
// Novel command: imports blame — for one import job, who got in, who was
// skipped, and why; paired with the provider-side SMS delivery reasons.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// pp:data-source live
func newNovelImportsBlameCmd(flags *rootFlags) *cobra.Command {
	var flagRows int

	cmd := &cobra.Command{
		Use:   "blame [jobId]",
		Short: "Who got in, who was skipped, and why — for one import job",
		Long:  "Joins the import job's row outcomes (created/updated/skipped/error, grouped by reason) with the account's SMS delivery report (provider rejections + Conduyt skips like landline_skipped/no_phone_skipped) so a bulk push can be blamed end-to-end in one call — the reconstruction the Kloudi incident required by hand.",
		Example: strings.Trim(`
  conduyt-crm-pp-cli imports blame 3f2a9c1e-0000-0000-0000-000000000000
  conduyt-crm-pp-cli imports blame 3f2a9c1e-0000-0000-0000-000000000000 --agent --select rowOutcomes,smsDelivery.rejectionReasons`, "\n"),
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if dryRunOK(flags) {
				return nil
			}
			jobID := args[0]
			c, err := flags.newClient()
			if err != nil {
				return err
			}
			data, err := c.Get(cmd.Context(), "/imports/"+jobID, nil)
			if err != nil {
				return classifyAPIError(err, flags)
			}
			var envelope map[string]any
			if err := json.Unmarshal(data, &envelope); err != nil {
				return fmt.Errorf("parsing import job: %w", err)
			}
			job, ok := envelope["data"].(map[string]any)
			if !ok {
				job = envelope
			}
			out := map[string]any{
				"jobId": jobID, "fileName": job["fileName"], "status": job["status"],
				"rowOutcomes": map[string]any{
					"total": job["totalRows"], "created": job["createdRows"], "updated": job["updatedRows"],
					"skipped": job["skippedRows"], "duplicates": job["duplicateRows"], "errors": job["errorRows"],
				},
			}
			// Group row-level errors by message so "why" is a table, not a scroll.
			if rowsData, err := c.Get(cmd.Context(), "/imports/"+jobID+"/rows", map[string]string{"per_page": fmt.Sprint(flagRows), "status": "error"}); err == nil {
				var renv map[string]any
				if json.Unmarshal(rowsData, &renv) == nil {
					reasons := map[string]int{}
					walk := func(items []any) {
						for _, it := range items {
							if m, ok := it.(map[string]any); ok {
								msg, _ := m["error"].(string)
								if msg == "" {
									msg, _ = m["message"].(string)
								}
								if msg == "" {
									msg = "(no reason recorded)"
								}
								if len(msg) > 120 {
									msg = msg[:120]
								}
								reasons[msg]++
							}
						}
					}
					if d, ok := renv["data"].(map[string]any); ok {
						if items, ok := d["data"].([]any); ok {
							walk(items)
						} else if items, ok := d["rows"].([]any); ok {
							walk(items)
						}
					} else if items, ok := renv["data"].([]any); ok {
						walk(items)
					}
					if len(reasons) > 0 {
						out["errorReasons"] = reasons
					}
				}
			}
			// Provider-side lens: what the SMS provider did with the pushes.
			if repData, err := c.Get(cmd.Context(), "/reports/sms-delivery", nil); err == nil {
				var rep map[string]any
				if json.Unmarshal(repData, &rep) == nil {
					if d, ok := rep["data"].(map[string]any); ok {
						out["smsDelivery"] = map[string]any{"totals": d["totals"], "rejectionReasons": d["rejectionReasons"]}
					}
				}
			} else {
				out["smsDeliveryError"] = err.Error()
			}
			return printJSONFiltered(cmd.OutOrStdout(), out, flags)
		},
	}
	cmd.Flags().IntVar(&flagRows, "rows", 200, "How many error rows to sample when grouping reasons")
	return cmd
}
