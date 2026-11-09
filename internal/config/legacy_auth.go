// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

package config

// PATCH(novel1219-auth-compat): auth compatibility with installs of the earlier library entry, which read the
// CONDUYT_BEARER_AUTH and CONDUYT_API_KEY env vars and saved the token as bearer_auth / api_key in the config and
// credentials files. The current CLI reads CONDUYT_CRM_BEARER_AUTH and crm_bearer_auth. Legacy env names stay
// fallbacks (with a deprecation notice), and a saved legacy key is honoured and rewritten to crm_bearer_auth once
// (atomic replace, the old file kept beside it as a backup). No secret value is ever printed.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
	"github.com/ptaramona/conduyt-crm-cli/internal/cliutil"
)

// legacyAuthEnvNames are the env vars the earlier entry read, in fallback order after CONDUYT_CRM_BEARER_AUTH.
var legacyAuthEnvNames = []string{"CONDUYT_BEARER_AUTH", "CONDUYT_API_KEY"}

// legacyAuthFileKeys are the saved keys the earlier entry wrote, in the same precedence (bearer_auth, then api_key).
var legacyAuthFileKeys = []string{"bearer_auth", "api_key"}

const currentAuthFileKey = "crm_bearer_auth"

// legacyAuthNoticeWriter receives the one-line notices; tests replace it.
var legacyAuthNoticeWriter io.Writer = os.Stderr

var legacyAuthNoticeOnce sync.Map

func legacyAuthNotice(key, format string, args ...any) {
	if _, seen := legacyAuthNoticeOnce.LoadOrStore(key, true); seen {
		return
	}
	fmt.Fprintf(legacyAuthNoticeWriter, "notice: "+format+"\n", args...)
}

// legacyEnvBearer returns the first legacy env var that carries a token, with its name.
func legacyEnvBearer() (name, value string) {
	for _, n := range legacyAuthEnvNames {
		if v := cliutil.EnvOverride(n); strings.TrimSpace(v) != "" {
			return n, v
		}
	}
	return "", ""
}

// applyLegacyEnvBearer is the env fallback: CONDUYT_CRM_BEARER_AUTH (applied by Load) wins; otherwise
// CONDUYT_BEARER_AUTH, then CONDUYT_API_KEY, each with a one-line deprecation notice.
func (c *Config) applyLegacyEnvBearer() {
	if strings.TrimSpace(cliutil.EnvOverride("CONDUYT_CRM_BEARER_AUTH")) != "" {
		return
	}
	name, v := legacyEnvBearer()
	if name == "" {
		return
	}
	c.ConduytCrmBearerAuth = v
	c.markEnvOverride("ConduytCrmBearerAuth")
	c.AuthSource = "env:" + name
	c.CredentialSource = "env:" + name
	legacyAuthNotice("env:"+name, "%s is deprecated; set CONDUYT_CRM_BEARER_AUTH instead (the value is used for now)", name)
}

// legacyAuthFileCandidates are the files a legacy install may hold a saved token in: the config file this run
// resolves (and, for the default layout, the pre-paths legacy config) plus the credentials file beside it.
func legacyAuthFileCandidates(path string, explicit bool) []string {
	out := []string{path}
	if explicit {
		if creds, err := cliutil.CredentialsFilePathForConfig(path); err == nil {
			out = append(out, creds)
		}
		return out
	}
	if legacy, err := LegacyConfigPath(); err == nil && legacy != path {
		out = append(out, legacy)
	}
	if creds, err := cliutil.CredentialsFilePath(); err == nil {
		out = append(out, creds)
	}
	return out
}

// migrateLegacyAuthFiles rewrites every owner-only candidate file that still carries a legacy key. It returns the
// legacy token of a file it could not rewrite, so Load can still honour it in memory for this run.
func migrateLegacyAuthFiles(candidates []string) string {
	unmigrated := ""
	for _, path := range candidates {
		token, err := migrateLegacyAuthFile(path)
		if err != nil {
			legacyAuthNotice("migrate-failed:"+path, "could not migrate the saved legacy credential in %s to %s (%v); it is used for this run only", path, currentAuthFileKey, err)
			if unmigrated == "" {
				unmigrated = token
			}
		}
	}
	return unmigrated
}

// migrateLegacyAuthFile moves a saved bearer_auth / api_key value in one TOML file to crm_bearer_auth. A file with
// no legacy key is left untouched (idempotent). A file that is not a regular, owner-only file is never rewritten:
// the CLI does not trust credentials from it anyway. On a rewrite error the legacy token is returned with the error.
func migrateLegacyAuthFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil
	}
	// The same owner-only check the CLI applies before trusting a credentials file (POSIX mode and owner, macOS
	// ACLs, Windows DACLs); a file it would not trust is never rewritten.
	if cliutil.VerifyCredsPerms(path) != nil {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	doc := map[string]any{}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return "", nil // the normal load reports the parse error
	}
	legacyKey, token := "", ""
	for _, k := range legacyAuthFileKeys {
		if v, ok := doc[k].(string); ok && strings.TrimSpace(v) != "" {
			legacyKey, token = k, v
			break
		}
	}
	hasLegacy := false
	for _, k := range legacyAuthFileKeys {
		if _, ok := doc[k]; ok {
			hasLegacy = true
		}
	}
	if !hasLegacy {
		return "", nil
	}
	current, _ := doc[currentAuthFileKey].(string)
	if strings.TrimSpace(current) == "" && token != "" {
		doc[currentAuthFileKey] = token
	}
	for _, k := range legacyAuthFileKeys {
		delete(doc, k)
	}
	out, err := toml.Marshal(doc)
	if err != nil {
		return token, fmt.Errorf("encoding: %w", err)
	}
	backup := path + ".legacy-auth.bak"
	if _, statErr := os.Lstat(backup); os.IsNotExist(statErr) {
		if err := cliutil.AtomicWritePrivateFile(backup, raw, 0o600, 0o700); err != nil {
			return token, fmt.Errorf("writing backup: %w", err)
		}
	}
	if err := cliutil.AtomicWritePrivateFile(path, out, 0o600, 0o700); err != nil {
		return token, fmt.Errorf("rewriting: %w", err)
	}
	if legacyKey != "" && strings.TrimSpace(current) == "" {
		legacyAuthNotice("migrated:"+path, "migrated the saved %s in %s to %s (backup: %s)", legacyKey, path, currentAuthFileKey, backup)
	} else {
		legacyAuthNotice("migrated:"+path, "removed the superseded legacy credential key from %s; %s is kept (backup: %s)", path, currentAuthFileKey, backup)
	}
	return "", nil
}
