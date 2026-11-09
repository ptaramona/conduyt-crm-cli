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
//   - Nothing is ever rewritten. One notice names the file and the command that re-saves the token.
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
	legacyAuthNotice("key:"+src.Path, "%s saves the token under the earlier key %s; it is used as is. Re-save it under crm_bearer_auth with '%s' (the CLI does not rewrite the file)", src.Path, src.Key, legacyResaveCommand)
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

// dropLegacyBearer runs at the start of SaveTokens: when the bearer in effect came from an earlier saved key, it is
// cleared so the newly saved token is the one sent, and the earlier value is never persisted as crm_bearer_auth.
func (c *Config) dropLegacyBearer() {
	if c.legacyKey == nil || c.ConduytCrmBearerAuth != c.legacyKey.Token {
		return
	}
	c.ConduytCrmBearerAuth = ""
	c.updateFileConfigField("ConduytCrmBearerAuth")
	c.legacyKey = nil
}

// scrubLegacyCredentialFiles runs after a successful SaveTokens: the earlier keys are removed from the explicit
// profile's credentials file and the global one, through cliutil's checked-descriptor read and private writer (the
// config document drops them on every save already, since only current fields are marshalled).
func (c *Config) scrubLegacyCredentialFiles() error {
	if c.AgentcookieManagedByExternalStore() {
		return nil
	}
	if c.explicitConfig {
		if path, err := cliutil.CredentialsFilePathForConfig(c.Path); err == nil {
			if err := cliutil.ScrubLegacyCredentialKeys(path, true); err != nil {
				return err
			}
		}
	}
	if path, err := cliutil.CredentialsFilePath(); err == nil {
		if err := cliutil.ScrubLegacyCredentialKeys(path, false); err != nil {
			return err
		}
	}
	return nil
}

// LegacyAuthEnvInEffect names the legacy env var that supplies the token (the same rule Load applies), or "".
// Logout reports it like CONDUYT_CRM_BEARER_AUTH: an env credential cannot be removed by logout.
func LegacyAuthEnvInEffect() string {
	if envNameDecides("CONDUYT_CRM_BEARER_AUTH") {
		return ""
	}
	name, _ := legacyEnvBearer()
	return name
}
