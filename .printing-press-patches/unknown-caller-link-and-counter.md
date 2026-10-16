# Unknown-caller prompt (board #35): calls link, calls count-unknown-callers, contacts create --call-id, dialer get-history --unmatched/--line (2026-10-15)

App main f132ed6f0 (live 2026-10-15 9:45 PM PT). The public OpenAPI at that commit is the contract; only this feature's operations were taken.

- `spec.yaml`: new `POST /calls/{id}/link` (linkCallToContact) and `GET /calls/unknown-callers` (countUnknownCallers), copied verbatim from the public
  OpenAPI; `POST /contacts` request body is `allOf` ContactCreate + `callId` (uuid) with the public description and the 409/422 refusals; `GET /dialer/history`
  gains only the `unmatched` and `line` query params (its other public params, q/recorded_only/etc., were NOT added: out of scope for this lane).
- `upstream/openapi.yaml` (the drift gate's snapshot of the public API, taken at the 829-operation reprint): only the two new path blocks were added so
  `TestSpecMatchesUpstreamPublicAPI` stays honest for this feature. The snapshot was NOT refreshed wholesale: the public API at f132ed6f0 has 82 more
  operations than this spec (chat, help, Mailchimp, Zoom, WordPress forms, pool filter sets, email signature, and more); refreshing it fails the gate until
  a full reprint is done, which is a separate lane.
- Generation: `printing-press` 4.32.1 on the old and the new spec.yaml into scratch dirs (gen-old, gen-new), then the difference applied. The tree is still the
  4.28.0 print, whose generated files differ in shape from 4.32.1 output (no HeaderParams/Mutating in code_orch, `classifyAPIError(err, flags)`, `Changed || body != ""`
  became `body != ""`, no wrapPlatformStructuredOutput, `printOutputWithFlagsMeta` without the trailing nil). So `calls_count-unknown-callers.go`, `calls_link.go`,
  `calls_link_call-to-contact.go` are the 4.32.1 files translated to the 4.28.0 helpers; the hunks for `contacts_create.go`, `dialer_get-history.go`, `calls.go`,
  `code_orch.go`, `tools.go` and `tools-manifest.json` were applied by hand in the 4.28.0 style. A reprint under the current Press must produce the same commands.
- Not applied from the 4.32.1 delta: the `sync.go`, `store.go`, `learn_init.go` and store test additions for a synced "link" table (the generator treated
  `/calls/{id}/link` as a syncable sub-resource; a POST acknowledgement is not data to sync), and its "Use since for incremental fetches" query tip in
  `tools.go` (a false positive: `since` here is the counter's window start, not a modification-time filter).
- Command shapes as generated: `calls count-unknown-callers --line --since`, `calls link call-to-contact <id> --contact-id [--attach-number] [--dry-run]`,
  `contacts create --call-id <uuid>`, `dialer get-history --unmatched 1 --line <n>`. On `calls link call-to-contact`, the local `--dry-run` flag shadows the root
  `--dry-run`, so it sends the API's body `dryRun: true` (the unknown-caller preview) and does not trigger the Press's request-printing dry run; pinned by a test.
  `calls` stays a hidden group, as in the 4.28.0 print.
- `tools-manifest.json` 727 -> 728 tools (the link tool; the counter was already listed); `mcp.tool_count` 726 -> 728 in `internal/mcp/tools.go` plus the calls group lists.
- Tests: `internal/cli/unknown_caller_test.go`.
