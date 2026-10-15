# contacts list --email: spec.yaml carries the new `email` query param (2026-10-15)

- `spec.yaml` `/contacts` GET gains the public `email` param exactly as the app's `public/openapi.yaml` documents it (exploded
  form-style array, max 20, exact match on the primary and secondary email slots; app main 318adf38d). It sits next to `SearchParam`.
- `internal/cli/contacts_list.go`, `internal/cli/helpers.go` (`appendArrayQueryParam`), `internal/mcp/tools.go` and `tools-manifest.json`
  are the Printing Press 4.32.1 output for that one spec change (generated from the old and the new spec.yaml into scratch directories and
  the difference applied here, so the 4.28.0 print and its hand-fixes are untouched). `--email` takes a comma list or a JSON array and
  sends one `email` param per address. A repeated `--email a --email b` keeps only the last value (the generator emits a string flag).
- `internal/mcp/code_orch.go` was NOT updated: the 4.32.1 template differs from this tree's 4.28.0 file in the hunks that carry array
  query params, and it holds hand-fixes. The code-orchestration surface therefore does not bind `email` yet; the endpoint-mirror tools do.
- `upstream/openapi.yaml` was left stale on purpose (the public document has grown by ~13,000 lines since the 2026-09-16 reprint, and
  `make sync-spec` would fail TestSpecMatchesUpstreamPublicAPI until a full reprint); that refresh is a separate change.
- `internal/cli/contacts_list_email_test.go` pins the flag, the repeated params (comma and JSON forms) and the no-flag case.
