# dialer patch-settings: typed body with wrapupAutoReleaseSeconds (2026-10-15)

- `spec.yaml` `/dialer/settings` PATCH now carries the typed request body exactly as the app's `public/openapi.yaml` documents it at app main
  ce2d3cdff (live 2026-10-15 8:59 AM PT): autoAdvanceEnabled, pauseSeconds (1..60), advanceOn, openLeadOnDial, smartDialCallerId and the new
  `wrapupAutoReleaseSeconds` (0 = off, otherwise 30..3600; default 300). Only the two dialer settings operations were looked at; GET is unchanged.
- `internal/cli/dialer_patch-settings.go` and `tools-manifest.json` are the Printing Press 4.32.1 output for that one spec change (generated from the
  old and the new spec.yaml into scratch directories, the difference applied here), so the 4.28.0 print and its hand-fixes are untouched. The
  diff between the two scratch trees was exactly those two files. New flags: `--wrapup-auto-release-seconds` (and the typed `--pause-seconds`,
  `--advance-on`, `--auto-advance-enabled`, `--open-lead-on-dial`, `--smart-dial-caller-id`). A flag that was passed is always sent, so `0` (off) is
  never dropped. The 0-or-30..3600 rule is the API's (400); the CLI does not duplicate it.
- Hand-fix: the generator cut the flag help at the first ".." of "30..3600" (it printed "otherwise 30.."); the usage text is now
  "...0 = off, otherwise 30 to 3600 (default 300)". A reprint must keep this (pinned by `TestDialerPatchSettingsWrapupFlagIsDocumented`).
- `internal/mcp/code_orch.go` needs no change: code orchestration binds path and query params only, and the PATCH body is passed through as given.
- The scratch `printing-press generate` runs ended on the generator's own govulncheck gate (Go 1.26.6 stdlib advisory in the scratch toolchain),
  after the project was written; its own `go test ./...` step passed. This tree's gates are unchanged.
- No version bump: this repo carries no version of its own (manifest.json stays 1.0.0 and the CHANGELOG is release-automation owned); the print
  version stays 4.28.0 in `.printing-press.json`, as the earlier parity lane left it.
- Tests: `internal/cli/dialer_patch_settings_test.go` (0, 30, 300, 3600 sent as numbers; rides with other flags; flag help; manifest param).
