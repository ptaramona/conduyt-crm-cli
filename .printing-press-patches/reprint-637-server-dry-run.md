# --server-dry-run: the global --dry-run never sends (board #637 round 3, 2026-10-30)

The CLI's global `--dry-run` (root persistent flag) prints the request and sends nothing. Printing Press 4.28.0 prints an operation's
`dry_run` query parameter as a LOCAL `--dry-run` flag, which shadows the global one: on the full public document 161 commands (153 that
existed before the reprint) would have sent a real request with `?dry_run=true` when a user asked for a local preview.

A reprint must keep:
- Every generated `dry_run` query flag is registered as `--server-dry-run` (same variable, same wire name `dry_run`), help "ask the server
  to validate without saving; the request IS sent ...". 161 commands, marked `// PATCH(reprint-637-server-dry-run)`.
- The two body-level server previews are renamed the same way: `calls link call-to-contact --server-dry-run` (body `dryRun: true`;
  the unknown-caller note's old local `--dry-run`) and `imports repair post-imports-id --server-dry-run` (body `dry_run`).
- `import <resource>` no longer registers its own `--dry-run`; the global flag (already copied onto the client) previews it the same way.
- No command registers a local `--dry-run`.
- email send / send-bulk keep their hand-written flag set (email-send-live-keys-cli-324.md): they never had a local `--dry-run`
  and expose the API's dry_run only through `--stdin` / MCP, so they need no line here; the test exempts exactly these two operations.
- MCP: the executor and tools-manifest.json keep the wire name `dry_run` (there is no global preview on the MCP surface to shadow, and
  the email body-key guard reads those names); the 161 manifest descriptions say the request IS sent and name the CLI flag.

Pinned by `internal/cli/server_dry_run_test.go` (`TestNoLocalDryRunAndServerDryRunIsBound`): it walks the whole command tree, fails on
any local `--dry-run`, requires `--server-dry-run` on every command whose spec.yaml operation has a dry_run query parameter (163 minus
the two email operations), and runs each of them with the global `--dry-run` against a counting server that must see zero requests.

Upstream issue (for the board, not filed): never emit a local flag whose name a persistent root flag already uses.
