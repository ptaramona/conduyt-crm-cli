// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

package config

// PATCH(novel1219-auth-compat): auth compatibility with installs of the earlier library entry, read-only.
//
//   - Env: CONDUYT_CRM_BEARER_AUTH, then CONDUYT_BEARER_AUTH, then CONDUYT_API_KEY; a legacy name prints one
//     deprecation line.
//   - Saved keys: the earlier entry's bearer_auth / api_key are additional field names of the same documents the
//     CLI already loads. The config file's are read inside parseConfigData (bytes from cliutil.ReadOpenedConfig's
//     checked descriptor, refused with the same permission rule); the credentials files' are read by the existing
//     checked-descriptor credential loader (cliutil.Credentials), where they count as credentials, so an unsafe
//     file is refused and that refusal suppresses the global fallback exactly as for crm_bearer_auth. A document
//     that carries crm_bearer_auth uses it only; within a document the normal field precedence (auth_header, then
//     the bearer token, then the OAuth token) is unchanged.
//   - Load never writes. The existing writers write only the current key: auth set-token (SaveBearerToken) writes
//     crm_bearer_auth = <token> and auth logout (ClearTokens) writes crm_bearer_auth = "" into the files the loader
//     reads for the scope; the earlier keys stay where they are and become unreachable. No file is ever deleted by
//     this code. One notice names the file and the re-save command.
//   - No secret value is ever printed.

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

// legacyResaveCommand re-saves the token under the current key.
const legacyResaveCommand = "conduyt-crm-pp-cli auth set-token"

// legacyAuthNoticeWriter receives the one-line notices; tests replace it.
var legacyAuthNoticeWriter io.Writer = os.Stderr

var legacyAuthNoticeOnce sync.Map

func legacyAuthNotice(key, format string, args ...any) {
	if _, seen := legacyAuthNoticeOnce.LoadOrStore(key, true); seen {
		return
	}
	fmt.Fprintf(legacyAuthNoticeWriter, "notice: "+format+"\n", args...)
}

// legacyKeySource is the saved earlier key that supplied the token.
type legacyKeySource struct {
	Path  string
	Key   string
	Token string
}

// applyLegacyConfigKey runs inside parseConfigData over the bytes of the checked descriptor: a config document with
// no crm_bearer_auth but an earlier key supplies the token there, so acceptOpenedConfig's permission refusal and
// untrusted-routing drop apply to it exactly as to the current key.
func (c *Config) applyLegacyConfigKey(data []byte, path string) {
	if c.ConduytCrmBearerAuth != "" {
		return
	}
	var doc struct {
		BearerAuth string `toml:"bearer_auth"`
		APIKey     string `toml:"api_key"`
	}
	var keys map[string]any
	if toml.Unmarshal(data, &doc) != nil || toml.Unmarshal(data, &keys) != nil {
		return
	}
	if _, present := keys["crm_bearer_auth"]; present {
		return // the current key is present (even empty, an explicitly cleared token): earlier keys are ignored
	}
	creds := cliutil.Credentials{LegacyBearerAuth: doc.BearerAuth, LegacyAPIKey: doc.APIKey}
	creds.ApplyLegacyKeys()
	if creds.LegacyKey == "" {
		return
	}
	c.ConduytCrmBearerAuth = creds.ConduytCrmBearerAuth
	c.legacyKey = &legacyKeySource{Path: path, Key: creds.LegacyKey, Token: creds.ConduytCrmBearerAuth}
}

// noteLegacyKey prints one notice when an earlier saved key supplied the token still in effect.
func (c *Config) noteLegacyKey() {
	src := c.legacyKey
	if src == nil || c.ConduytCrmBearerAuth != src.Token {
		c.legacyKey = nil
		return
	}
	legacyAuthNotice("key:"+src.Path, "%s saves the token under the earlier key %s; it is used as is. Re-save it with '%s' (that writes crm_bearer_auth, after which the earlier key is ignored)", src.Path, src.Key, legacyResaveCommand)
}

// envNameDecides reports whether an env var is set to a real value, including an empty or blank one: a set name
// decides, so a blank value means "explicitly cleared" and never falls through to a later name. An unset name, or an
// unresolved MCP bundle placeholder (a host that did not configure it), does not decide.
func envNameDecides(name string) bool {
	v, set := os.LookupEnv(name)
	return set && !cliutil.UnresolvedUserConfigPlaceholder(v)
}

// legacyEnvBearer returns the legacy env var that supplies the token, with its value: the first legacy name that is
// set decides, and a blank one supplies nothing.
func legacyEnvBearer() (name, value string) {
	for _, n := range legacyAuthEnvNames {
		if !envNameDecides(n) {
			continue
		}
		if v := cliutil.EnvOverride(n); strings.TrimSpace(v) != "" {
			return n, v
		}
		return "", ""
	}
	return "", ""
}

// applyLegacyEnvBearer is the env fallback: CONDUYT_CRM_BEARER_AUTH (applied by Load) wins; otherwise
// CONDUYT_BEARER_AUTH, then CONDUYT_API_KEY, each with a one-line deprecation notice.
func (c *Config) applyLegacyEnvBearer() {
	if envNameDecides("CONDUYT_CRM_BEARER_AUTH") { // set, even blank: the current name decides
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

// SaveBearerToken is what auth set-token saves: the token as crm_bearer_auth, the top bearer key, through the existing
// saver (global credentials file, config document), and, for an explicit profile whose own credentials file exists,
// into that file too (cliutil.SetProfileBearer), since the loader reads it first. The file-layer snapshot and the
// env override marker are updated directly, so an env overlay never makes the save persist the old value. Earlier
// keys are not removed: a present crm_bearer_auth makes them unreachable.
func (c *Config) SaveBearerToken(token string) error {
	c.ConduytCrmBearerAuth = token
	delete(c.envOverrides, "ConduytCrmBearerAuth")
	c.updateFileConfigField("ConduytCrmBearerAuth")
	c.legacyKey = nil
	if err := c.saveCredentialsThenConfig(); err != nil {
		return err
	}
	if c.explicitConfig && !c.AgentcookieManagedByExternalStore() {
		return cliutil.SetProfileBearer(c.Path, token)
	}
	return nil
}

// EffectiveAuthEnv names the env var that supplies the token under Load's own rules, or "" when none does:
// CONDUYT_CRM_BEARER_AUTH decides when it is set (an unresolved MCP placeholder does not count as set) and supplies a
// token when its effective value is non-empty; otherwise the first set legacy name decides. Logout reports exactly
// this name.
func EffectiveAuthEnv() string {
	if envNameDecides("CONDUYT_CRM_BEARER_AUTH") {
		if cliutil.EnvOverride("CONDUYT_CRM_BEARER_AUTH") != "" {
			return "CONDUYT_CRM_BEARER_AUTH"
		}
		return ""
	}
	name, _ := legacyEnvBearer()
	return name
}
