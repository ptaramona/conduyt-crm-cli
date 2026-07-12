# Acceptance Report: conduyt-crm
Level: Full Dogfood (1,791-test matrix) + Quick gate rerun
Full matrix: 1,710/1,791 passed (95.5%); quick gate 16/16 PASS (runner-written).
Failure classification (81, spot-checked live — NO CLI defects found):
- Rate-limit noise from the 1,791-call barrage against prod (429s) — workflows list, automations get-failures etc. all PASS when run individually live.
- OAuth browser-callback endpoints (google/microsoft/slack callbacks) — not CLI-invokable by design.
- Dark/gated features: verify-line-type 403 (lineTypeVerificationEnabled OFF — expected), SCIM/SSO (enterprise dark launch).
- Oversized export: contacts export → clean 413 with the API's row-cap message (207,784 > 50k) — correct behavior.
- Missing fixtures (bogus UUIDs on get-by-id probes) → clean typed 404s.
Novel commands live-verified against BDA prod: send-check (real verdict incl. "line type not verified yet" warn), imports watch (typed 404 + poll loop), verify-line-type (expected 403 while dark), imports blame, drips audit (store-based), analytics (framework), tail (framework).
Fixes applied in shipcheck loops: narrative command shapes (get-sms-delivery, preflight --stdin, analytics --type/--group-by), SKILL OPTIONS-endpoint drift, "$JOB" quoting.
Printing Press issues for retro: sample-output probe runs without the session env auth (all live probes 401 in shipcheck); full-level live dogfood needs per-source rate-limit pacing against small tenant APIs.
Gate: PASS
