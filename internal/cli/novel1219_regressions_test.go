// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

// PATCH(novel1219-regressions): one regression per Greptile finding on PR #2450 (round 1: 6 P1 + 2 P2), each against
// the behaviour the reprint's simple novel commands had and the public library entry's hardened ones restore.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ptaramona/conduyt-crm-cli/internal/cliutil/testenv"
	"github.com/ptaramona/conduyt-crm-cli/internal/store"
)

// Greptile P1 "Smart-list checks fail": members are read through GET /contacts?smartListId, never the add/remove-only
// /smart-lists/{id}/contacts route.
func TestNovel1219SendCheckListReadsMembersThroughTheContactsRoute(t *testing.T) {
	testenv.Isolate(t)
	const list = "7c1d2e3f-0000-4000-8000-000000000000"
	var paths []string
	out, _, err := runNovel(t, []string{"send-check", "--list", list, "--json"}, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/contacts":
			if r.URL.Query().Get("smartListId") != list {
				t.Errorf("contacts read without smartListId: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"c1","phone":"+12125550101","lineType":"mobile"}],"meta":{"total":1}}`))
		case "/contacts/dnc-status":
			writeDNCResponse(t, w, r)
		default:
			http.NotFound(w, r)
		}
	})
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "/smart-lists/") {
			t.Fatalf("send-check called the add/remove-only members route: %v", paths)
		}
	}
	if !strings.Contains(compactTestJSON(out), `"verdict":"go"`) {
		t.Fatalf("out=%s", out)
	}
}

// sendCheckTagAudience serves n tagged contacts, 200 per page; the LAST contact is SMS-blocked in dnc-status.
func sendCheckTagAudience(t *testing.T, n int, pages *atomic.Int32) http.HandlerFunc {
	last := fmt.Sprintf("c-%d", n-1)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/contacts":
			pages.Add(1)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			start := (page - 1) * perPage
			items := []map[string]any{}
			for i := start; i < n && i < start+perPage; i++ {
				items = append(items, map[string]any{"id": fmt.Sprintf("c-%d", i), "phone": fmt.Sprintf("+1212555%04d", i), "lineType": "mobile"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": items, "meta": map[string]any{"total": n}})
		case "/contacts/dnc-status":
			writeDNCResponse(t, w, r, last)
		default:
			http.NotFound(w, r)
		}
	}
}

// Greptile P1 "Unchecked contacts pass the gate": a tag of 201 contacts is paged past page 1, so a blocked 201st
// contact makes the verdict no_go (exit 2) instead of a go over the first 200.
func TestNovel1219SendCheckTagPaginatesPastPageOne(t *testing.T) {
	testenv.Isolate(t)
	var pages atomic.Int32
	out, _, err := runNovel(t, []string{"send-check", "--tag", "big", "--limit", "500", "--json"}, sendCheckTagAudience(t, 201, &pages))
	var view sendCheckView
	if decodeErr := json.Unmarshal([]byte(out), &view); decodeErr != nil {
		t.Fatalf("out=%s", out)
	}
	if ExitCode(err) != 2 || view.Verdict != "no_go" || view.Checked != 201 || view.Summary.DNC != 1 || pages.Load() != 2 {
		t.Fatalf("err=%v code=%d verdict=%s checked=%d dnc=%d pages=%d", err, ExitCode(err), view.Verdict, view.Checked, view.Summary.DNC, pages.Load())
	}
}

// Greptile P1 "Unchecked contacts pass the gate", second half: when --limit leaves contacts unchecked the result is
// inconclusive and non-zero, never a go.
func TestNovel1219SendCheckLimitLeavingContactsUncheckedIsInconclusive(t *testing.T) {
	testenv.Isolate(t)
	var pages atomic.Int32
	out, _, err := runNovel(t, []string{"send-check", "--tag", "big", "--limit", "200", "--json"}, sendCheckTagAudience(t, 201, &pages))
	var view sendCheckView
	if decodeErr := json.Unmarshal([]byte(out), &view); decodeErr != nil {
		t.Fatalf("out=%s", out)
	}
	if ExitCode(err) != 5 || view.Verdict != "inconclusive" || !view.Partial || view.Checked != 200 || view.Total == nil || *view.Total != 201 {
		t.Fatalf("err=%v code=%d view=%+v", err, ExitCode(err), view)
	}
}

// Greptile P1 "DNC checks miss blocked numbers": DNC verdicts come from the contact-specific dnc-status lookup, so a
// contact whose number only shares the last 7 digits with a blocked one is NOT blocked, and the /dnc list is never read.
func TestNovel1219SendCheckRefusesALastSevenDigitMatch(t *testing.T) {
	testenv.Isolate(t)
	var paths []string
	out, _, err := runNovel(t, []string{"send-check", "--tag", "pair", "--json"}, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/contacts":
			// Same last 7 digits (555-0101), different area codes: only "blocked" is on the DNC list.
			_, _ = w.Write([]byte(`{"data":[{"id":"blocked","phone":"+12125550101","lineType":"mobile"},{"id":"clean","phone":"+13105550101","lineType":"mobile"}],"meta":{"total":2}}`))
		case "/contacts/dnc-status":
			writeDNCResponse(t, w, r, "blocked")
		default:
			http.NotFound(w, r)
		}
	})
	var view sendCheckView
	if decodeErr := json.Unmarshal([]byte(out), &view); decodeErr != nil {
		t.Fatalf("out=%s", out)
	}
	verdicts := map[string]string{}
	for _, v := range view.Verdicts {
		verdicts[v.ContactID] = v.Verdict
	}
	if ExitCode(err) != 2 || verdicts["blocked"] != "dnc" || verdicts["clean"] != "ok" {
		t.Fatalf("err=%v verdicts=%v", err, verdicts)
	}
	for _, p := range paths {
		if p == "/dnc" {
			t.Fatalf("send-check read the paged /dnc list: %v", paths)
		}
	}
}

// Greptile P1 "DNC checks miss blocked numbers", completeness: a dnc-status answer that omits a requested contact is
// inconclusive, never a clean verdict for it.
func TestNovel1219SendCheckIncompleteDNCAnswerIsInconclusive(t *testing.T) {
	testenv.Isolate(t)
	out, _, err := runNovel(t, []string{"send-check", "--tag", "pair", "--json"}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/contacts":
			_, _ = w.Write([]byte(`{"data":[{"id":"a","phone":"+12125550101","lineType":"mobile"},{"id":"b","phone":"+13105550102","lineType":"mobile"}],"meta":{"total":2}}`))
		case "/contacts/dnc-status":
			_, _ = w.Write([]byte(`{"data":{"statuses":{"a":{"dnc":false,"litigator":false,"voiceBlocked":false,"smsBlocked":false}},"requested":2,"checked":1,"missing":1,"truncated":false}}`))
		default:
			http.NotFound(w, r)
		}
	})
	if ExitCode(err) != 5 || !strings.Contains(compactTestJSON(out), `"verdict":"inconclusive"`) {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

// Greptile P1 "Import error counts are wrong": blame reads the documented error tab with a page size the route allows
// (at most 100), and an ordinary row is never counted as an error "(no reason recorded)".
func TestNovel1219ImportsBlameReadsTheErrorTabWithASupportedPageSize(t *testing.T) {
	testenv.Isolate(t)
	var queries []string
	out, _, err := runNovel(t, []string{"imports", "blame", "job", "--json"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/imports/job/rows" {
			queries = append(queries, r.URL.Query().Get("tab")+":"+r.URL.Query().Get("per_page"))
		}
		blameHandler(t, "", blameErrorRows([]map[string]any{blameErrorRow(1, "invalid phone")}, 1, nil), "")(w, r)
	})
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if len(queries) != 1 || queries[0] != "error:100" {
		t.Fatalf("rows queries=%v, want one error-tab page of 100", queries)
	}
	if c := compactTestJSON(out); !strings.Contains(c, `"errors_by_reason":{"invalid phone":1}`) || strings.Contains(c, "no reason recorded") {
		t.Fatalf("out=%s", c)
	}
}

func watchTestHandler(t *testing.T, side func(w http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/imports/job":
			_, _ = w.Write([]byte(`{"data":{"id":"job","status":"completed","totalRows":1,"createdRows":1,"updatedRows":0,"skippedRows":0,"errorRows":0,"processedRows":1}}`))
		case "/imports/job/launch-monitor":
			_, _ = w.Write([]byte(`{"data":{"stage":"complete"}}`))
		case "/imports/job/side-effects":
			side(w)
		case "/reports/sms-delivery":
			t.Error("--verify must read the job-scoped side effects, not the account-wide delivery report")
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}

// Greptile P1 "Failed verification reports success": a verification read that fails, or side effects with queued or
// failed sends, make `imports watch --verify` partial and non-zero; complete side effects verify with exit 0.
func TestNovel1219ImportsWatchVerifyFailsNonZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		side func(w http.ResponseWriter)
		code int
		want string
	}{
		{"side effects fetch fails", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound) // not retried; the failed fetch keeps its typed class (3)
			_, _ = w.Write([]byte(`{"error":"gone"}`))
		}, 3, "missing job side effects"},
		{"queued sends", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"data":{"total":2,"completed":1,"failed":0,"retryableFailed":0,"exhaustedFailed":0,"queued":1,"held":0,"actionRequired":false,"isComplete":false}}`))
		}, 5, "side effects are not complete"},
		{"failed sends", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"data":{"total":2,"completed":1,"failed":1,"retryableFailed":1,"exhaustedFailed":0,"queued":0,"held":0,"actionRequired":false,"isComplete":false}}`))
		}, 5, "side effects are not complete"},
		{"complete", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"data":{"total":1,"completed":1,"failed":0,"retryableFailed":0,"exhaustedFailed":0,"queued":0,"held":0,"actionRequired":false,"isComplete":true}}`))
		}, 0, `"verified":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.Isolate(t)
			out, _, err := runNovel(t, []string{"imports", "watch", "job", "--verify", "--interval", "1", "--json"}, watchTestHandler(t, tc.side))
			code := 0
			if err != nil {
				code = ExitCode(err)
			}
			if code != tc.code || !strings.Contains(compactTestJSON(out), tc.want) {
				t.Fatalf("err=%v code=%d out=%s", err, code, out)
			}
		})
	}
}

// --interval/--timeout accept both the public entry's durations and the reprint's bare seconds.
func TestNovel1219ImportsWatchIntervalTakesDurationsAndSeconds(t *testing.T) {
	for _, value := range []string{"10", "10s", "1m"} {
		var d secondsOrDuration
		if err := d.Set(value); err != nil || d <= 0 {
			t.Fatalf("Set(%q) = %v, %v", value, d, err)
		}
	}
	var d secondsOrDuration
	if d.Set("ten") == nil {
		t.Fatal("Set(ten) accepted a non-duration")
	}
	if err := d.Set("10"); err != nil || d.String() != "10s" {
		t.Fatalf("bare seconds = %s, %v", d.String(), err)
	}
}

// Greptile P1 "Different messages look duplicated": two different bodies to one contact that share an 80-character
// introduction are not a double send; the same full body twice is.
func TestNovel1219DripsAuditGroupsByTheFullBody(t *testing.T) {
	testenv.Isolate(t)
	dbPath := t.TempDir() + "/data.db"
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	intro := strings.Repeat("Hi Sam, this is a reminder about your appointment with our office. ", 2)
	for id, body := range map[string]string{"m1": intro + "Reply C to confirm.", "m2": intro + "Reply R to reschedule.", "m3": "Same body", "m4": "Same body"} {
		contact := "c-intro"
		if id == "m3" || id == "m4" {
			contact = "c-dup"
		}
		raw, _ := json.Marshal(map[string]any{"id": id, "contactId": contact, "direction": "outbound", "channel": "sms", "body": body, "createdAt": "2026-07-02T10:00:00Z"})
		if err := db.Upsert("messages", id, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out, _, err := runNovel(t, []string{"drips", "audit", "--db", dbPath, "--json"}, nil)
	if ExitCode(err) != 2 {
		t.Fatalf("err=%v out=%s, want exit 2 for the one real double send", err, out)
	}
	var got struct {
		Summary     map[string]any   `json:"summary"`
		DoubleSends []map[string]any `json:"doubleSends"`
	}
	if json.Unmarshal([]byte(out), &got) != nil || len(got.DoubleSends) != 1 || got.DoubleSends[0]["contactId"] != "c-dup" || got.DoubleSends[0]["count"] != float64(2) {
		t.Fatalf("out=%s, want only c-dup flagged", out)
	}
}

// Greptile P2 "Cost estimate can undercount": a mirror that exists but was never synced is not counted as the
// audience; the estimate counts live instead, and an incomplete live count is a partial lower bound with a non-zero exit.
func TestNovel1219VerifyLineTypeEstimateNeverTrustsAnUnsyncedMirror(t *testing.T) {
	testenv.Isolate(t)
	dbPath := t.TempDir() + "/data.db"
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var contactsReads atomic.Int32
	out, _, err := runNovel(t, []string{"contacts", "verify-line-type", "--estimate", "--db", dbPath, "--json"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/contacts" {
			http.NotFound(w, r)
			return
		}
		contactsReads.Add(1)
		// One short page while the metadata reports 5 contacts: the count is a lower bound.
		_, _ = w.Write([]byte(`{"data":[{"id":"c1","phone":"+12125550101"}],"meta":{"total":5}}`))
	})
	var view verifyEstimate
	if decodeErr := json.Unmarshal([]byte(out), &view); decodeErr != nil {
		t.Fatalf("out=%s", out)
	}
	if contactsReads.Load() == 0 || view.Synced || ExitCode(err) != 5 || !view.Partial || view.Unverified != 1 {
		t.Fatalf("err=%v reads=%d view=%+v", err, contactsReads.Load(), view)
	}
	testenv.Isolate(t)
	table, _, _ := runNovel(t, []string{"contacts", "verify-line-type", "--estimate", "--db", dbPath, "--human-friendly"}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"c1","phone":"+12125550101"}],"meta":{"total":5}}`))
	})
	if !strings.Contains(table, "lower bound") {
		t.Fatalf("human estimate does not label the partial count a lower bound: %s", table)
	}
}

// Greptile P2 "Stalled verification keeps running": a batch with missing fields, or one whose remaining does not
// decrease, stops the paid loop at once instead of re-requesting up to --max-batches.
func TestNovel1219VerifyLineTypeRunStopsOnAnInvalidOrStalledBatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		batches []string
		posts   int32
		want    string
	}{
		{"missing fields", []string{`{"data":{"done":false}}`}, 1, "missing or invalid verified number"},
		{"stalled", []string{`{"data":{"done":false,"verified":50,"remaining":100}}`, `{"data":{"done":false,"verified":0,"remaining":100}}`}, 2, "verification made no progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.Isolate(t)
			var posts atomic.Int32
			out, _, err := runNovel(t, []string{"contacts", "verify-line-type", "run", "--until-done", "--json"}, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/contacts/verify-line-type" {
					http.NotFound(w, r)
					return
				}
				n := posts.Add(1)
				body := tc.batches[len(tc.batches)-1]
				if int(n) <= len(tc.batches) {
					body = tc.batches[n-1]
				}
				_, _ = w.Write([]byte(body))
			})
			if ExitCode(err) != 5 || posts.Load() != tc.posts || !strings.Contains(out, tc.want) {
				t.Fatalf("err=%v posts=%d out=%s", err, posts.Load(), out)
			}
		})
	}
}

// The reprint's --contact-ids scope stays available on the paid loop and is sent as contactIds.
func TestNovel1219VerifyLineTypeRunSendsContactIDs(t *testing.T) {
	testenv.Isolate(t)
	var body map[string]any
	_, _, err := runNovel(t, []string{"contacts", "verify-line-type", "run", "--until-done", "--contact-ids", "a,b", "--json"}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"data":{"done":true,"verified":2,"remaining":0}}`))
	})
	if err != nil || fmt.Sprint(body["contactIds"]) != "[a b]" {
		t.Fatalf("err=%v body=%v", err, body)
	}
	testenv.Isolate(t)
	if _, _, err := runNovel(t, []string{"contacts", "verify-line-type", "run", "--until-done", "--contact-ids", "a", "--smart-list", "x"}, nil); ExitCode(err) != 2 {
		t.Fatalf("--contact-ids with --smart-list: err=%v, want a usage error", err)
	}
}

// The terminal job's `errors` field is its issue log (an array), never a row counter: watch reads errorRows only, so a
// job carrying an issue log is not "missing invalid errors" (seen live on the demo workspace).
func TestNovel1219ImportsWatchIgnoresTheJobsIssueLog(t *testing.T) {
	testenv.Isolate(t)
	out, _, err := runNovel(t, []string{"imports", "watch", "job", "--verify", "--interval", "1", "--json"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/imports/job" {
			_, _ = w.Write([]byte(`{"data":{"id":"job","status":"completed","totalRows":1,"createdRows":0,"updatedRows":1,"skippedRows":0,"errorRows":0,"processedRows":1,"errors":[{"row":1,"code":"info","message":"junk email dropped"}]}}`))
			return
		}
		watchTestHandler(t, func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"data":{"total":0,"completed":0,"failed":0,"retryableFailed":0,"exhaustedFailed":0,"queued":0,"held":0,"actionRequired":false,"isComplete":false}}`))
		})(w, r)
	})
	if c := compactTestJSON(out); err != nil || !strings.Contains(c, `"verified":true`) || !strings.Contains(c, `"no_side_effects":true`) || strings.Contains(c, "invalid errors") {
		t.Fatalf("err=%v out=%s", err, c)
	}
}
