// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
// Novel command: imports watch — poll an import job to completion, then render
// the outcome recap with typed exit codes (cron/n8n-safe).
package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// pp:data-source live
func newNovelImportsWatchCmd(flags *rootFlags) *cobra.Command {
	var flagVerify bool
	var flagInterval int
	var flagTimeout int

	cmd := &cobra.Command{
		Use:   "watch [jobId]",
		Short: "Poll an import job to completion, then render the outcome recap",
		Long:  "Polls the import job until it reaches a terminal state, then prints the row-outcome recap (created/updated/skipped/errors). With --verify, also fetches the SMS delivery report so provider rejections and Conduyt skips are visible in the same breath. Typed exit codes: 0 completed clean, 2 completed with error rows, 4 failed/timeout — drop it straight into n8n or cron.",
		Example: strings.Trim(`
  conduyt-crm-pp-cli imports watch 3f2a9c1e-0000-0000-0000-000000000000 --verify
  conduyt-crm-pp-cli imports watch 3f2a9c1e-0000-0000-0000-000000000000 --interval 10 --json`, "\n"),
		Annotations: map[string]string{"mcp:read-only": "true", "pp:typed-exit-codes": "0,2,4"},
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
			deadline := time.Now().Add(time.Duration(flagTimeout) * time.Second)
			var job map[string]any
			for {
				data, err := c.GetNoCache(cmd.Context(), "/imports/"+jobID, nil)
				if err != nil {
					return classifyAPIError(err, flags)
				}
				var envelope map[string]any
				if err := json.Unmarshal(data, &envelope); err != nil {
					return fmt.Errorf("parsing import job: %w", err)
				}
				if d, ok := envelope["data"].(map[string]any); ok {
					job = d
				} else {
					job = envelope
				}
				status, _ := job["status"].(string)
				if status == "completed" || status == "failed" {
					break
				}
				if time.Now().After(deadline) {
					fmt.Fprintf(cmd.ErrOrStderr(), "timeout after %ds — job still %q\n", flagTimeout, status)
					return &cliError{code: 4, err: fmt.Errorf("watch timeout")}
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "status=%s processed=%v/%v\n", status, job["processedRows"], job["totalRows"])
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(time.Duration(flagInterval) * time.Second):
				}
			}
			recap := map[string]any{
				"jobId":     jobID,
				"status":    job["status"],
				"totalRows": job["totalRows"], "processedRows": job["processedRows"],
				"createdRows": job["createdRows"], "updatedRows": job["updatedRows"],
				"skippedRows": job["skippedRows"], "errorRows": job["errorRows"],
			}
			if flagVerify {
				if data, err := c.Get(cmd.Context(), "/reports/sms-delivery", nil); err == nil {
					var rep map[string]any
					if json.Unmarshal(data, &rep) == nil {
						if d, ok := rep["data"].(map[string]any); ok {
							recap["smsDelivery"] = map[string]any{"totals": d["totals"], "rejectionReasons": d["rejectionReasons"]}
						}
					}
				} else {
					recap["smsDeliveryError"] = err.Error()
				}
			}
			if err := printJSONFiltered(cmd.OutOrStdout(), recap, flags); err != nil {
				return err
			}
			status, _ := job["status"].(string)
			if status == "failed" {
				return &cliError{code: 4, err: fmt.Errorf("import failed")}
			}
			if n, ok := job["errorRows"].(float64); ok && n > 0 {
				return &cliError{code: 2, err: fmt.Errorf("%d error rows", int(n))}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagVerify, "verify", false, "After completion, include the SMS delivery report (provider rejections + Conduyt skips)")
	cmd.Flags().IntVar(&flagInterval, "interval", 5, "Poll interval in seconds")
	cmd.Flags().IntVar(&flagTimeout, "timeout", 1800, "Give up after this many seconds (exit 4)")
	return cmd
}
