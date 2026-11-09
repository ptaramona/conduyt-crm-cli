// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// importRowsMaxPerPage is the ceiling GET /imports/{id}/rows enforces on per_page (the route clamps above it).
const importRowsMaxPerPage = 100

// PATCH(novel1219-imports-blame): the public library entry's hardened blame (strict job-scoped side-effect counters,
// isComplete/actionRequired checks, full pagination with a progress guard and a metadata-total cross-check, partial
// results exiting non-zero) ported back over the reprint's simple version, reading the documented error tab
// (tab=error, per_page at most 100) and grouping only error rows by their full message. --latest and the [jobId]
// Use line stay from the reprint.
// pp:data-source live
func newNovelImportsBlameCmd(flags *rootFlags) *cobra.Command {
	var limit, rowsAlias int
	var latest bool
	cmd := &cobra.Command{Use: "blame [jobId]", Short: "For one import job: who got the SMS, who was skipped and why", Long: "Use this command after an import finished: it reads the job's row counters, every error row (GET /imports/{id}/rows?tab=error, paged to --limit) grouped by reason, and the job-scoped side effects (queued, retryable, exhausted, held, completed). A capped, short or failed read makes the result partial and the exit 5. Do NOT use it to wait for an import to finish; use 'imports watch' instead.", Example: strings.Trim(`
  conduyt-crm-pp-cli imports blame --latest --json
  conduyt-crm-pp-cli imports blame 3f2a9c1e-0000-0000-0000-000000000000
  conduyt-crm-pp-cli imports blame 3f2a9c1e-0000-0000-0000-000000000000 --agent --select row_outcomes,errors_by_reason`, "\n"), Annotations: map[string]string{"mcp:read-only": "true", "pp:data-source": "live", "pp:typed-exit-codes": "0,2,3,5"}, RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && !latest && !cmd.Flags().Changed("limit") && !cmd.Flags().Changed("rows") {
			return cmd.Help()
		}
		if cmd.Flags().Changed("rows") && !cmd.Flags().Changed("limit") {
			limit = rowsAlias
		}
		if limit <= 0 {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("--limit must be positive"))
		}
		if len(args) > 0 && latest {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("pass a jobId or --latest, not both"))
		}
		if len(args) > 1 {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("imports blame takes one jobId"))
		}
		if !latest && (len(args) == 0 || strings.TrimSpace(args[0]) == "") {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("jobId (or --latest) is required"))
		}
		if dryRunOK(flags) {
			return writeDryRun(cmd.OutOrStdout(), flags, "imports blame") // PATCH(gocli1219-live-gate-defects)
		}
		ctx, cancel := boundCtx(cmd.Context(), flags)
		defer cancel()
		c, err := flags.newClient()
		if err != nil {
			return err
		}
		var jobID string
		if latest {
			newest, latestErr := resolveLatestImportJob(ctx, c)
			if latestErr != nil {
				return classifyAPIError(cmd.OutOrStdout(), latestErr, flags)
			}
			if newest == "" {
				return printJSONFiltered(cmd.OutOrStdout(), noImportJobYet(), flags)
			}
			jobID = newest
		} else {
			jobID = args[0]
		}
		failures := 0
		var fetchErr error
		failureReasons := make([]string, 0)

		// 1) The job and its row counters. A job that does not exist is the API's 404 (exit 3).
		jobRaw, e := c.Get(ctx, "/imports/"+jobID, nil)
		if e != nil {
			return classifyAPIError(cmd.OutOrStdout(), e, flags)
		}
		job := map[string]any{}
		if decodeErr := rejectResponseErrorEnvelope(jobRaw); decodeErr != nil {
			failures++
			failureReasons = append(failureReasons, "decoding import job: "+decodeErr.Error())
		} else {
			var env map[string]any
			if decodeErr := json.Unmarshal(jobRaw, &env); decodeErr != nil {
				failures++
				failureReasons = append(failureReasons, "decoding import job: "+decodeErr.Error())
			} else {
				job = unwrapMap(env)
			}
		}
		outcomes := blameRowOutcomes{Status: strAny(job, "status")}
		missingCounters := make([]string, 0)
		for _, spec := range []struct {
			preferred string
			legacy    []string
			target    **int
		}{
			{"totalRows", []string{"total_rows"}, &outcomes.Total},
			{"createdRows", []string{"created_rows"}, &outcomes.Created},
			{"updatedRows", []string{"updated_rows"}, &outcomes.Updated},
			{"skippedRows", []string{"skipped_rows"}, &outcomes.Skipped},
			{"duplicateRows", []string{"duplicate_rows"}, &outcomes.Duplicates},
			{"errorRows", []string{"error_rows"}, &outcomes.Errors},
		} {
			value, counterErr := preferredLegacyCounter(job, spec.preferred, spec.legacy...)
			if counterErr != nil {
				missingCounters = append(missingCounters, counterErr.Error())
				continue
			}
			*spec.target = value
		}
		if outcomes.Status == "" {
			missingCounters = append(missingCounters, "status")
		}
		if outcomes.Errors == nil {
			missingCounters = append(missingCounters, "errorRows")
		}
		if len(missingCounters) > 0 {
			sort.Strings(missingCounters)
			failures++
			failureReasons = append(failureReasons, "import job missing or invalid fields: "+strings.Join(missingCounters, ", "))
		}

		// 2) Every error row (the route's error tab), paged to --limit.
		rows := make([]map[string]any, 0)
		perPage := minInt(importRowsMaxPerPage, limit+1)
		var total *int
		rowTabs := map[string]int(nil)
		progress := newPaginationProgressGuard()
		for page := 1; len(rows) <= limit; page++ {
			data, e := c.Get(ctx, "/imports/"+jobID+"/rows", map[string]string{"tab": "error", "page": strconv.Itoa(page), "per_page": strconv.Itoa(perPage)})
			if e != nil {
				fetchErr = e
				failures++
				failureReasons = append(failureReasons, fmt.Sprintf("fetching import error rows page %d: %v", page, e))
				break
			}
			batch, decodeErr := objectItems(data, false)
			if decodeErr != nil {
				failures++
				failureReasons = append(failureReasons, fmt.Sprintf("decoding import error rows page %d: %v", page, decodeErr))
				break
			}
			ids := make([]string, len(batch))
			for i, row := range batch {
				if ids[i], decodeErr = importRowIdentity(row, page, i); decodeErr != nil {
					failures++
					failureReasons = append(failureReasons, fmt.Sprintf("decoding import error rows page %d: %v", page, decodeErr))
					break
				}
			}
			if decodeErr != nil {
				break
			}
			if n, ok := responseTotal(data); ok {
				total = &n
			}
			if page == 1 {
				rowTabs = importRowTabs(data)
			}
			if guardErr := progress.observe(ids, "", len(batch) == perPage); guardErr != nil {
				failures++
				failureReasons = append(failureReasons, guardErr.Error())
				break
			}
			rows = append(rows, batch...)
			if len(batch) < perPage {
				break
			}
		}
		capped := len(rows) > limit || total != nil && *total > limit
		if total != nil && *total > len(rows) && len(rows) <= limit {
			failures++
			failureReasons = append(failureReasons, fmt.Sprintf("import error rows ended after %d rows but response metadata reports total %d", len(rows), *total))
		}
		if capped {
			failureReasons = append(failureReasons, fmt.Sprintf("--limit %d capped the error rows checked; re-run with a higher --limit", limit))
		}
		if len(rows) > limit {
			rows = rows[:limit]
		}
		rowsUnavailable := false
		rowsNote := ""
		if rowTabs != nil && total != nil && rowTabs["error"] > *total {
			rowsUnavailable = true
			rowsNote = fmt.Sprintf("%d error rows were counted but only %d were itemized by the import; the rest carry no per-row reason", rowTabs["error"], *total)
		}
		errorsByReason := map[string]int{}
		for i, r := range rows {
			outcome, statusErr := preferredLegacyString(r, "outcome", "status")
			if statusErr != nil {
				failures++
				failureReasons = append(failureReasons, fmt.Sprintf("import error row %d outcome: %v", i+1, statusErr))
				continue
			}
			if !strings.EqualFold(outcome, "error") {
				// tab=error must only return error rows; anything else is never counted as an error.
				failures++
				failureReasons = append(failureReasons, fmt.Sprintf("import error row %d has outcome %q, not error", i+1, outcome))
				continue
			}
			reason := strings.TrimSpace(strAny(r, "message", "error", "reason"))
			if reason == "" {
				reason = "unknown"
			}
			errorsByReason[reason]++
		}

		// 3) The job-scoped side effects (delivery state of the import's sends).
		side := blameSideEffects{}
		sideRaw, e := c.Get(ctx, "/imports/"+jobID+"/side-effects", nil)
		if e != nil {
			if fetchErr == nil {
				fetchErr = e
			}
			failures++
			failureReasons = append(failureReasons, fmt.Sprintf("fetching import side effects: %v", e))
		} else {
			var env map[string]any
			if decodeErr := rejectResponseErrorEnvelope(sideRaw); decodeErr != nil {
				failures++
				failureReasons = append(failureReasons, "decoding import side effects: "+decodeErr.Error())
			} else if decodeErr := json.Unmarshal(sideRaw, &env); decodeErr == nil {
				m := unwrapMap(env)
				missing := make([]string, 0, 7)
				for name, spec := range map[string]struct {
					key    string
					legacy string
					target *int
				}{"queued": {"queued", "", &side.Queued}, "retryable": {"retryableFailed", "retryable", &side.Retryable}, "exhausted": {"exhaustedFailed", "exhausted", &side.Exhausted}, "total": {"total", "", &side.Total}, "completed": {"completed", "", &side.Completed}, "failed": {"failed", "", &side.Failed}, "held": {"held", "", &side.Held}} {
					value, counterErr := nonNegativeCounter(m, spec.key)
					if spec.legacy != "" {
						value, counterErr = preferredLegacyCounter(m, spec.key, spec.legacy)
					}
					if value == nil {
						if counterErr != nil {
							missing = append(missing, counterErr.Error())
						} else {
							missing = append(missing, name)
						}
						continue
					}
					*spec.target = *value
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					failures++
					failureReasons = append(failureReasons, "import side effects missing or invalid counters: "+strings.Join(missing, ", "))
				}
				actionRequired, actionPresent, actionErr := preferredLegacyBool(m, "actionRequired", "action_required")
				if actionErr != nil {
					failures++
					failureReasons = append(failureReasons, "import side effects "+actionErr.Error())
				} else if actionPresent {
					side.ActionRequired = &actionRequired
				}
				isComplete, isCompleteErr := requiredBool(m, "isComplete")
				if isCompleteErr != nil {
					failures++
					failureReasons = append(failureReasons, "import side effects "+isCompleteErr.Error())
				} else {
					side.IsComplete = isComplete
					side.NoSideEffects = !isComplete && len(missing) == 0 && side.Total == 0 && side.Queued == 0 && side.Retryable == 0 && side.Exhausted == 0 && side.Failed == 0 && side.Held == 0
					if !isComplete && !side.NoSideEffects {
						failures++
						failureReasons = append(failureReasons, "import side effects are not complete")
					}
				}
			} else {
				failures++
				failureReasons = append(failureReasons, fmt.Sprintf("decoding import side effects: %v", decodeErr))
			}
		}
		view := blameView{
			JobID:                     jobID,
			RowOutcomes:               outcomes,
			ErrorRows:                 len(rows),
			Partial:                   failures > 0 || capped,
			Checked:                   len(rows),
			Total:                     total,
			Delivered:                 side.Completed,
			DeliveryCorrelation:       "none",
			DeliveryCorrelationReason: "GET /reports/sms-delivery returns aggregate totals only (no per-message import, message or contact keys), so delivery is read from the job-scoped side-effect counters",
			ErrorsByReason:            errorsByReason,
			SideEffects:               side,
			RowTabs:                   rowTabs,
			RowsUnavailable:           rowsUnavailable,
			RowsNote:                  rowsNote,
			FetchFailures:             failures,
			Failures:                  failureReasons,
		}
		if failures > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %d import blame read(s) failed or were incomplete\n", failures)
		}
		if !wantsHumanTable(cmd.OutOrStdout(), flags) {
			if e := printJSONFiltered(cmd.OutOrStdout(), view, flags); e != nil {
				return e
			}
			return blameExit(view, fetchErr)
		}
		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		if view.Partial {
			fmt.Fprintln(tw, "WARNING: result is partial: "+strings.Join(view.Failures, "; "))
		}
		actionRequired := "unknown"
		if side.ActionRequired != nil {
			actionRequired = strconv.FormatBool(*side.ActionRequired)
		}
		fmt.Fprintf(tw, "JOB\tSTATUS\tERROR ROWS\tDELIVERED\tQUEUED\tRETRYABLE\tEXHAUSTED\tHELD\tACTION REQUIRED\n%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n", view.JobID, outcomes.Status, view.ErrorRows, view.Delivered, side.Queued, side.Retryable, side.Exhausted, side.Held, actionRequired)
		reasons := make([]string, 0, len(errorsByReason))
		for reason := range errorsByReason {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		for _, reason := range reasons {
			fmt.Fprintf(tw, "ERROR\t%s\t%d\n", reason, errorsByReason[reason])
		}
		if e := tw.Flush(); e != nil {
			return e
		}
		return blameExit(view, fetchErr)
	}}
	cmd.Flags().IntVar(&limit, "limit", 500, "Maximum error rows to inspect")
	cmd.Flags().IntVar(&rowsAlias, "rows", 500, "Deprecated alias of --limit")
	_ = cmd.Flags().MarkHidden("rows")
	cmd.Flags().BoolVar(&latest, "latest", false, "Use the account's most recent import job instead of a jobId")
	return cmd
}

func blameExit(view blameView, fetchErr error) error {
	if !view.Partial {
		return nil
	}
	if fetchErr != nil {
		return classifyAPIErrorOnly(fetchErr)
	}
	return apiErr(fmt.Errorf("imports blame is incomplete: %s", strings.Join(view.Failures, "; ")))
}

// importRowIdentity is the stable identity of one GET /imports/{id}/rows entry: its source row number, else a
// contact identity. A row with neither cannot be paged or counted safely.
func importRowIdentity(row map[string]any, page, index int) (string, error) {
	if n, ok := numericInt(row["row"]); ok && n > 0 {
		return "row:" + strconv.Itoa(n), nil
	}
	if id := strings.TrimSpace(strAny(row, "contactId", "contact_id", "id")); id != "" {
		return id, nil
	}
	if phone, ok := normalizePhoneIdentity(strAny(row, "phone", "normalizedPhone")); ok {
		return "phone:" + phone, nil
	}
	return "", fmt.Errorf("import row %d on page %d has no row number or contact identity", index+1, page)
}

type blameRowOutcomes struct {
	Status     string `json:"status"`
	Total      *int   `json:"total"`
	Created    *int   `json:"created"`
	Updated    *int   `json:"updated"`
	Skipped    *int   `json:"skipped"`
	Duplicates *int   `json:"duplicates"`
	Errors     *int   `json:"errors"`
}

type blameSideEffects struct {
	Queued         int   `json:"queued"`
	Retryable      int   `json:"retryable"`
	Exhausted      int   `json:"exhausted"`
	Total          int   `json:"total"`
	Completed      int   `json:"completed"`
	Failed         int   `json:"failed"`
	Held           int   `json:"held"`
	ActionRequired *bool `json:"action_required"`
	IsComplete     bool  `json:"is_complete"`
	// NoSideEffects: the job recorded none (every counter zero); the route reports isComplete=false for that.
	NoSideEffects bool `json:"no_side_effects,omitempty"`
}
type blameView struct {
	JobID                     string           `json:"job_id"`
	RowOutcomes               blameRowOutcomes `json:"row_outcomes"`
	ErrorRows                 int              `json:"error_rows"`
	Partial                   bool             `json:"partial"`
	Checked                   int              `json:"checked"`
	Total                     *int             `json:"total,omitempty"`
	Delivered                 int              `json:"delivered"`
	DeliveryCorrelation       string           `json:"delivery_correlation"`
	DeliveryCorrelationReason string           `json:"delivery_correlation_reason"`
	ErrorsByReason            map[string]int   `json:"errors_by_reason"`
	SideEffects               blameSideEffects `json:"side_effects"`
	RowTabs                   map[string]int   `json:"row_tabs,omitempty"`
	RowsUnavailable           bool             `json:"rows_unavailable"`
	RowsNote                  string           `json:"rows_note,omitempty"`
	FetchFailures             int              `json:"fetch_failures"`
	Failures                  []string         `json:"failures,omitempty"`
}

func importRowTabs(raw json.RawMessage) map[string]int {
	var envelope struct {
		Data struct {
			Tabs map[string]int `json:"tabs"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	return envelope.Data.Tabs
}

func responseTotal(raw json.RawMessage) (int, bool) {
	var doc any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&doc) != nil {
		return 0, false
	}
	var find func(any) (int, bool)
	find = func(value any) (int, bool) {
		m, ok := value.(map[string]any)
		if !ok {
			return 0, false
		}
		for _, container := range []string{"meta", "pagination"} {
			if nested, ok := m[container].(map[string]any); ok {
				if n, ok := numericInt(nested["total"]); ok {
					return n, true
				}
			}
		}
		if n, ok := numericInt(m["total"]); ok {
			return n, true
		}
		if data, ok := m["data"]; ok {
			return find(data)
		}
		return 0, false
	}
	return find(doc)
}

func numericInt(value any) (int, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := strconv.Atoi(v.String())
		return n, err == nil
	case float64:
		if v < float64(-int(^uint(0)>>1)-1) || v > float64(int(^uint(0)>>1)) || v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func unwrapMap(m map[string]any) map[string]any {
	for {
		d, ok := m["data"].(map[string]any)
		if !ok {
			return m
		}
		m = d
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
