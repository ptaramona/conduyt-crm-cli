// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

import (
	"encoding/json"
	"fmt"
	"github.com/ptaramona/conduyt-crm-cli/internal/store"
	"github.com/spf13/cobra"
	"os"
	"strings"
	"text/tabwriter"
)

const verifyLineTypeUnitCostUSD = 0.008
const verifyLineTypeEstimateMaxPages = 100

// PATCH(novel1219-verify-line-type): the public library entry's split ported back over the reprint's single command:
// the bare command is a read-only estimate that checks the contacts mirror is synced, complete and fresh (else it
// counts live with full pagination) and labels an incomplete count a lower bound; the tenant-paid loop lives in
// 'run --until-done' and validates every batch (done/verified/remaining/byLineType) and stops when remaining fails
// to decrease. The reprint's --contact-ids scope is kept on 'run'.
// pp:data-source auto
func newNovelContactsVerifyLineTypeCmd(flags *rootFlags) *cobra.Command {
	var estimate, untilDone bool
	var smartList, dbPath string
	var maxBatches int
	cmd := &cobra.Command{Use: "verify-line-type", Short: "Estimate the cost of line-type verification without spending money", Long: "Performs a read-only count and cost estimate (~$0.008 per unverified number, billed to the tenant's own Twilio). It reads the local contacts mirror only when it is synced, complete and fresher than --max-age, and otherwise counts live; an incomplete count is reported as a lower bound and exits 5. --estimate is accepted as an explicit no-op. Use 'contacts verify-line-type run --until-done' for the tenant-paid verification loop. Do NOT use this command to decide whether a list is safe to text; use 'send-check' instead.", Example: strings.Trim(`
  conduyt-crm-pp-cli contacts verify-line-type --estimate
  conduyt-crm-pp-cli contacts verify-line-type --smart-list 7c1d2e3f-0000-0000-0000-000000000000 --estimate
  conduyt-crm-pp-cli contacts verify-line-type run --until-done --smart-list 7c1d2e3f-0000-0000-0000-000000000000`, "\n"), Annotations: map[string]string{"mcp:read-only": "true", "pp:data-source": "auto", "pp:happy-args": "--estimate=true"}, RunE: func(cmd *cobra.Command, args []string) error {
		if untilDone || cmd.Flags().Changed("max-batches") {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("--until-done and --max-batches moved to 'contacts verify-line-type run'"))
		}
		if dryRunOK(flags) {
			return writeDryRun(cmd.OutOrStdout(), flags, "contacts verify-line-type") // PATCH(gocli1219-live-gate-defects)
		}
		ctx, cancel := boundCtx(cmd.Context(), flags)
		defer cancel()
		if dbPath == "" {
			dbPath = defaultDBPath("conduyt-crm-pp-cli")
		}
		// PATCH(novel1219-verify-line-type) r2: the estimate follows --data-source. The mirror answers only when it is
		// synced, fresh, complete (decoded rows equal the sync total) and the scope is tenant-wide: smart-list
		// membership is not in synced contacts, so a smart-list estimate is always live. Under auto an unusable
		// mirror falls back to a live count; under local it is a partial lower bound with exit 5.
		var fetchErr error
		view := verifyEstimate{UnitCostUSD: verifyLineTypeUnitCostUSD}
		useLocal := false
		if flags.dataSource != "live" {
			local, reasons, err := verifyEstimateFromMirror(cmd, flags, dbPath, smartList)
			if err != nil {
				return err
			}
			switch {
			case len(reasons) == 0:
				view, useLocal = local, true
				flags.agentSource = "local"
			case flags.dataSource == "local":
				view = local
				view.Partial = true
				view.Failures = append(view.Failures, reasons...)
				useLocal = true
				flags.agentSource = "local"
			default:
				view.Hint = verifySyncHint(dbPath) // the mirror could not answer (reason); counted live
			}
		}
		if !useLocal {
			flags.agentSource = "live"
			c, err := flags.newClient()
			if err != nil {
				return err
			}
			progress := newPaginationProgressGuard()
			for page := 1; page <= verifyLineTypeEstimateMaxPages; page++ {
				params := map[string]string{"per_page": "200", "page": fmt.Sprint(page)}
				if smartList != "" {
					params["smartListId"] = smartList
				}
				raw, err := c.Get(ctx, "/contacts", params)
				if err != nil {
					fetchErr = err
					view.Partial = true
					view.Failures = append(view.Failures, fmt.Sprintf("fetching contacts estimate page %d: %v", page, err))
					break
				}
				items, decodeErr := objectItems(raw, false)
				if decodeErr != nil {
					view.Partial = true
					view.Failures = append(view.Failures, fmt.Sprintf("decoding contacts estimate page %d: %v", page, decodeErr))
					break
				}
				view.Checked += len(items)
				if total, ok := responseTotal(raw); ok {
					view.Total = &total
				}
				if guardErr := progress.observe(objectPaginationIDs(items), "", len(items) == 200); guardErr != nil {
					view.Partial = true
					view.Failures = append(view.Failures, guardErr.Error())
					break
				}
				for i, m := range items {
					phone := strings.TrimSpace(strAny(m, "phone"))
					if _, ok := normalizePhoneIdentity(phone); phone != "" && !ok {
						view.Partial = true
						view.Failures = append(view.Failures, fmt.Sprintf("contact estimate page %d row %d has an unmappable phone identity", page, i+1))
					} else if phone != "" && contactLineType(m) == "" {
						view.Unverified++
					}
				}
				if len(items) < 200 {
					break
				}
				if page == verifyLineTypeEstimateMaxPages {
					view.Partial = true
					view.Failures = append(view.Failures, "100-page safety cap reached")
				}
			}
			if view.Total != nil && view.Checked != *view.Total {
				view.Partial = true
				view.Failures = append(view.Failures, fmt.Sprintf("contacts estimate ended after %d rows but response metadata reports total %d", view.Checked, *view.Total))
			}
		}
		view.EstimatedCostUSD = float64(view.Unverified) * verifyLineTypeUnitCostUSD
		if err := printVerifyEstimate(cmd, flags, view); err != nil {
			return err
		}
		if view.Partial {
			if fetchErr != nil {
				return classifyAPIErrorOnly(fetchErr)
			}
			return apiErr(fmt.Errorf("line-type estimate is incomplete: %s", strings.Join(view.Failures, "; ")))
		}
		return nil
	}}
	cmd.Flags().BoolVar(&estimate, "estimate", false, "Count locally without spending money")
	cmd.Flags().StringVar(&smartList, "smart-list", "", "Limit to one smart list")
	cmd.Flags().BoolVar(&untilDone, "until-done", false, "Continue batches until verification is done")
	cmd.Flags().IntVar(&maxBatches, "max-batches", 400, "Safety cap on verification batches")
	cmd.Flags().StringVar(&dbPath, "db", "", "Local mirror path for estimates")
	cmd.AddCommand(newNovelContactsVerifyLineTypeRunCmd(flags))
	return cmd
}

func newNovelContactsVerifyLineTypeRunCmd(flags *rootFlags) *cobra.Command {
	var untilDone bool
	var smartList string
	var contactIDs []string
	var maxBatches int
	cmd := &cobra.Command{Use: "run", Short: "Run tenant-paid Twilio line-type verification", Long: "Drives the tenant-paid verification loop (~$0.008 per number, 50 lookups per batch) until done. Requires the account's Phone Line-Type Verification setting. Pass --until-done to acknowledge the paid operation. Every batch must carry done, verified and remaining; the loop stops at the first invalid batch or when remaining fails to decrease, and an unfinished run exits 5.", Example: strings.Trim(`
  conduyt-crm-pp-cli contacts verify-line-type run --until-done
  conduyt-crm-pp-cli contacts verify-line-type run --until-done --contact-ids 9e778307-e575-420b-b02c-dc9cc564febd --json`, "\n"), Annotations: map[string]string{"mcp:read-only": "false", "pp:data-source": "live", "pp:happy-args": "--until-done"}, RunE: func(cmd *cobra.Command, args []string) error {
		if !untilDone {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("--until-done is required"))
		}
		if maxBatches <= 0 {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("--max-batches must be positive"))
		}
		if smartList != "" && len(contactIDs) > 0 {
			_ = cmd.Usage()
			return usageErr(fmt.Errorf("pass --smart-list or --contact-ids, not both"))
		}
		if dryRunOK(flags) {
			return writeDryRun(cmd.OutOrStdout(), flags, "contacts verify-line-type run")
		}
		ctx, cancel := boundCtx(cmd.Context(), flags)
		defer cancel()
		c, err := flags.newClient()
		if err != nil {
			return err
		}
		body := map[string]any{}
		if smartList != "" {
			body["smartListId"] = smartList
		}
		if len(contactIDs) > 0 {
			body["contactIds"] = contactIDs
		}
		view := verifyRunView{ByLineType: map[string]int{}, Failures: []string{}}
		var runErr error
		previousRemaining := -1
		for view.Batches < maxBatches {
			raw, _, e := c.Post(ctx, "/contacts/verify-line-type", body)
			if e != nil {
				view.Partial = true
				view.Failures = append(view.Failures, fmt.Sprintf("verifying contact line types: %v", e))
				runErr = e
				break
			}
			if e = rejectResponseErrorEnvelope(raw); e != nil {
				view.Partial = true
				view.Failures = append(view.Failures, fmt.Sprintf("parsing verification response: %v", e))
				runErr = e
				break
			}
			var env map[string]any
			if e = json.Unmarshal(raw, &env); e != nil {
				view.Partial = true
				view.Failures = append(view.Failures, fmt.Sprintf("parsing verification response: %v", e))
				break
			}
			batch, e := parseVerifyBatch(env)
			if e != nil {
				view.Partial = true
				view.Failures = append(view.Failures, fmt.Sprintf("parsing verification response: %v", e))
				break
			}
			if batch.Done != (batch.Remaining == 0) {
				view.Partial = true
				view.Failures = append(view.Failures, fmt.Sprintf("parsing verification response: done is %t with %d remaining", batch.Done, batch.Remaining))
				break
			}
			if previousRemaining >= 0 && !batch.Done && batch.Remaining >= previousRemaining {
				view.Partial = true
				view.Failures = append(view.Failures, "parsing verification response: verification made no progress")
				break
			}
			view.Batches++
			view.Verified += batch.Verified
			for k, count := range batch.ByLineType {
				view.ByLineType[k] += count
			}
			view.Remaining, view.Done, previousRemaining = batch.Remaining, batch.Done, batch.Remaining
			if view.Done {
				break
			}
		}
		if !view.Done {
			view.Partial = true
			if len(view.Failures) == 0 {
				view.Failures = append(view.Failures, fmt.Sprintf("verification did not finish within %d batches", maxBatches))
			}
		}
		if !wantsHumanTable(cmd.OutOrStdout(), flags) {
			if err := printJSONFiltered(cmd.OutOrStdout(), view, flags); err != nil {
				return err
			}
		} else {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			if view.Partial {
				fmt.Fprintln(tw, "WARNING: verification is partial: "+strings.Join(view.Failures, "; "))
			}
			fmt.Fprintln(tw, "VERIFIED\tBATCHES\tREMAINING\tDONE")
			fmt.Fprintf(tw, "%d\t%d\t%d\t%t\n", view.Verified, view.Batches, view.Remaining, view.Done)
			if err := tw.Flush(); err != nil {
				return err
			}
		}
		if view.Partial {
			if runErr != nil {
				return classifyAPIErrorOnly(runErr)
			}
			return apiErr(fmt.Errorf("line-type verification is incomplete: %s", strings.Join(view.Failures, "; ")))
		}
		return nil
	}}
	cmd.Flags().BoolVar(&untilDone, "until-done", false, "Continue batches until verification is done")
	cmd.Flags().IntVar(&maxBatches, "max-batches", 400, "Safety cap on verification batches")
	cmd.Flags().StringVar(&smartList, "smart-list", "", "Limit to one smart list")
	cmd.Flags().StringSliceVar(&contactIDs, "contact-ids", nil, "Explicit contact UUIDs to verify (max 50,000)")
	return cmd
}

type verifyEstimate struct {
	Unverified       int      `json:"unverified"`
	UnitCostUSD      float64  `json:"unit_cost_usd"`
	EstimatedCostUSD float64  `json:"estimated_cost_usd"`
	Synced           bool     `json:"synced"`
	Hint             string   `json:"hint,omitempty"`
	Partial          bool     `json:"partial,omitempty"`
	Failures         []string `json:"failures,omitempty"`
	Checked          int      `json:"checked"`
	Total            *int     `json:"total,omitempty"`
}

func verifySyncHint(path string) string {
	return "run: conduyt-crm-pp-cli sync --resources contacts --db " + path
}

type verifyRunView struct {
	Verified   int            `json:"verified"`
	Batches    int            `json:"batches"`
	Remaining  int            `json:"remaining"`
	Done       bool           `json:"done"`
	ByLineType map[string]int `json:"by_line_type"`
	Partial    bool           `json:"partial"`
	Failures   []string       `json:"failures,omitempty"`
}

type verifyBatch struct {
	Verified   int
	Remaining  int
	Done       bool
	ByLineType map[string]int
}

func parseVerifyBatch(env map[string]any) (verifyBatch, error) {
	if _, ok := env["error"]; ok {
		return verifyBatch{}, fmt.Errorf("unexpected error envelope")
	}
	if _, ok := env["message"]; ok {
		return verifyBatch{}, fmt.Errorf("unexpected message envelope")
	}
	m := unwrapMap(env)
	if _, ok := m["error"]; ok {
		return verifyBatch{}, fmt.Errorf("unexpected error envelope")
	}
	if _, ok := m["message"]; ok {
		return verifyBatch{}, fmt.Errorf("unexpected message envelope")
	}
	done, ok := m["done"].(bool)
	if !ok {
		return verifyBatch{}, fmt.Errorf("missing or invalid done boolean")
	}
	verified, ok := numericInt(m["verified"])
	if !ok || verified < 0 {
		return verifyBatch{}, fmt.Errorf("missing or invalid verified number")
	}
	remaining, ok := numericInt(m["remaining"])
	if !ok || remaining < 0 {
		return verifyBatch{}, fmt.Errorf("missing or invalid remaining number")
	}
	batch := verifyBatch{Verified: verified, Remaining: remaining, Done: done, ByLineType: map[string]int{}}
	// PATCH(novel1219-verify-line-type) r2: byLineType is a required field of every batch (the route always sends it).
	raw, present := m["byLineType"]
	if !present {
		return verifyBatch{}, fmt.Errorf("missing byLineType object")
	}
	{
		by, ok := raw.(map[string]any)
		if !ok {
			return verifyBatch{}, fmt.Errorf("invalid byLineType: expected object of string to number")
		}
		for lineType, rawCount := range by {
			count, ok := numericInt(rawCount)
			if !ok || count < 0 {
				return verifyBatch{}, fmt.Errorf("invalid byLineType: expected object of string to number")
			}
			batch.ByLineType[lineType] = count
		}
	}
	return batch, nil
}

func printVerifyEstimate(cmd *cobra.Command, flags *rootFlags, v verifyEstimate) error {
	if !wantsHumanTable(cmd.OutOrStdout(), flags) {
		return printJSONFiltered(cmd.OutOrStdout(), v, flags)
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	if v.Partial {
		fmt.Fprintln(tw, "WARNING: estimate is partial; the unverified total and cost are lower bounds.")
	}
	fmt.Fprintln(tw, "UNVERIFIED\tUNIT COST USD\tESTIMATED COST USD")
	total := fmt.Sprint(v.Unverified)
	if v.Partial {
		total += "+ (lower bound)"
	}
	fmt.Fprintf(tw, "%s\t%.3f\t%.2f\n", total, v.UnitCostUSD, v.EstimatedCostUSD)
	return tw.Flush()
}

// verifyEstimateFromMirror counts unverified numbers in the local contacts mirror. reasons is empty only when the
// mirror can answer the estimate: tenant-wide scope, synced, fresher than --max-age, every row decoded and the
// decoded count equal to the sync total. Otherwise reasons say why and the returned view carries what was counted.
func verifyEstimateFromMirror(cmd *cobra.Command, flags *rootFlags, dbPath, smartList string) (verifyEstimate, []string, error) {
	view := verifyEstimate{UnitCostUSD: verifyLineTypeUnitCostUSD}
	if smartList != "" {
		view.Hint = verifySyncHint(dbPath)
		return view, []string{"smart-list membership is not stored in synced contacts"}, nil
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		view.Hint = verifySyncHint(dbPath)
		return view, []string{"no local contacts mirror"}, nil
	} else if err != nil {
		return view, nil, fmt.Errorf("checking local mirror: %w", err)
	}
	db, err := store.OpenWithContext(cmd.Context(), dbPath)
	if err != nil {
		return view, nil, fmt.Errorf("opening local mirror: %w", err)
	}
	defer db.Close()
	_, syncedAt, total, err := db.GetSyncState("contacts")
	if err != nil {
		return view, nil, fmt.Errorf("checking contacts sync state: %w", err)
	}
	view.Synced = !syncedAt.IsZero()
	if !view.Synced {
		view.Hint = verifySyncHint(dbPath)
		return view, []string{"contacts were never synced"}, nil
	}
	view.Total = &total
	reasons := make([]string, 0)
	if hintIfStale(cmd, db, "contacts", flags.maxAge) {
		reasons = append(reasons, "local contacts mirror is older than --max-age")
	}
	qr, err := db.DB().QueryContext(cmd.Context(), `SELECT id,data FROM resources WHERE resource_type='contacts'`)
	if err != nil {
		return view, nil, fmt.Errorf("reading contacts mirror: %w", err)
	}
	for qr.Next() {
		var id string
		var raw []byte
		if err = qr.Scan(&id, &raw); err != nil {
			_ = qr.Close()
			return view, nil, err
		}
		var m map[string]any
		if decodeErr := json.Unmarshal(raw, &m); decodeErr != nil {
			reasons = append(reasons, fmt.Sprintf("decoding mirrored contact %s: %v", id, decodeErr))
			continue
		}
		view.Checked++
		phone := strings.TrimSpace(strAny(m, "phone"))
		if _, ok := normalizePhoneIdentity(phone); phone != "" && !ok {
			view.Partial = true
			view.Failures = append(view.Failures, fmt.Sprintf("contact %s has an unmappable phone identity", id))
		} else if phone != "" && contactLineType(m) == "" {
			view.Unverified++
		}
	}
	if err = qr.Close(); err != nil {
		return view, nil, err
	}
	if err = qr.Err(); err != nil {
		return view, nil, err
	}
	if view.Checked != total {
		reasons = append(reasons, fmt.Sprintf("local contacts mirror contains %d decoded rows but sync state reports total %d", view.Checked, total))
	}
	return view, reasons, nil
}
