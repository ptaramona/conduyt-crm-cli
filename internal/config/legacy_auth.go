// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

package config

// PATCH(novel1219-auth-compat): auth compatibility with installs of the earlier library entry, which read the
// CONDUYT_BEARER_AUTH and CONDUYT_API_KEY env vars and saved the token as bearer_auth / api_key in the config file
// and the credentials files. The current CLI reads CONDUYT_CRM_BEARER_AUTH and crm_bearer_auth.
//
//   - Env: CONDUYT_CRM_BEARER_AUTH, then CONDUYT_BEARER_AUTH, then CONDUYT_API_KEY (legacy names print one
//     deprecation line).
//   - Saved keys are read IN PLACE through the same sources, order and trust rules as the current key: the config
//     file (parseConfigData, so an untrusted file's credentials are still dropped), then the profile's credentials
//     file, then the global credentials file (with the global-origin check for an explicit profile). The source
//     that supplies the token is recorded, so provenance and origin policy are the normal ones.
//   - Only the file whose legacy key actually supplied the token is then rewritten to crm_bearer_auth, once, where
//     an owner-only write can be guaranteed (POSIX); on Windows the legacy key keeps being read in place. The
//     rewrite runs under the credentials-file lock, re-reads the file and compares it with the bytes the load used
//     (never overwriting a value that changed), keeps a collision-free owner-only backup, and verifies every landed
//     file before the original is replaced.
//   - No secret value is ever printed.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/ptaramona/conduyt-crm-cli/internal/cliutil"
)

// legacyAuthEnvNames are the env vars the earlier entry read, in fallback order after CONDUYT_CRM_BEARER_AUTH.
var legacyAuthEnvNames = []string{"CONDUYT_BEARER_AUTH", "CONDUYT_API_KEY"}

// legacyAuthFileKeys are the saved keys the earlier entry wrote, in the same precedence (bearer_auth, then api_key).
var legacyAuthFileKeys = []string{"bearer_auth", "api_key"}

const currentAuthFileKey = "crm_bearer_auth"

// legacyAuthRewriteSupported reports whether this platform can rewrite a credential file with a guaranteed
// owner-only result. On Windows, chmod does not set a private DACL, so the legacy key is read in place instead.
// Tests override it to exercise both branches.
var legacyAuthRewriteSupported = func() bool { return runtime.GOOS != "windows" }

// legacyAuthNoticeWriter receives the one-line notices; tests replace it.
var legacyAuthNoticeWriter io.Writer = os.Stderr

var legacyAuthNoticeOnce sync.Map

func legacyAuthNotice(key, format string, args ...any) {
	if _, seen := legacyAuthNoticeOnce.LoadOrStore(key, true); seen {
		return
	}
	fmt.Fprintf(legacyAuthNoticeWriter, "notice: "+format+"\n", args...)
}

// legacyAuthSource is the file whose legacy key supplied the token, with the exact bytes the load parsed.
type legacyAuthSource struct {
	Path  string
	Key   string
	Token string
	Data  []byte
	Label string // "config" or "credentials file"
}

// legacyKeyIn returns the first non-empty legacy key in a TOML document, and whether the document already carries a
// non-empty current key (which then wins and the legacy key is not used).
func legacyKeyIn(data []byte) (key, token string, hasCurrent bool) {
	doc := map[string]any{}
	if toml.Unmarshal(data, &doc) != nil {
		return "", "", false
	}
	if v, ok := doc[currentAuthFileKey].(string); ok && strings.TrimSpace(v) != "" {
		hasCurrent = true
	}
	for _, k := range legacyAuthFileKeys {
		if v, ok := doc[k].(string); ok && strings.TrimSpace(v) != "" {
			return k, v, hasCurrent
		}
	}
	return "", "", hasCurrent
}

// applyLegacyConfigKey runs inside parseConfigData: a config file that has no crm_bearer_auth but a legacy key
// supplies the token through the normal path, so the caller's trust checks (permission refusal, untrusted routing)
// apply to it exactly as to the current key.
func (c *Config) applyLegacyConfigKey(data []byte, path string) {
	if c.ConduytCrmBearerAuth != "" {
		return
	}
	key, token, hasCurrent := legacyKeyIn(data)
	if hasCurrent || token == "" {
		return
	}
	c.ConduytCrmBearerAuth = token
	c.legacyAuth = &legacyAuthSource{Path: path, Key: key, Token: token, Data: append([]byte(nil), data...), Label: "config"}
}

// readLegacyCredentialsFile reads a credentials file the earlier entry wrote, applying the same owner-only check the
// CLI applies before trusting a credentials file. It returns nil when the file is absent, untrusted, or has no
// legacy key (or already has the current key, which the normal loader handles).
func readLegacyCredentialsFile(path string) (*legacyAuthSource, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", nil
	}
	if err := cliutil.VerifyCredsPerms(path); err != nil {
		return nil, "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	key, token, hasCurrent := legacyKeyIn(data)
	if hasCurrent || token == "" {
		return nil, "", nil
	}
	var origin struct {
		BaseURL string `toml:"base_url"`
	}
	_ = toml.Unmarshal(data, &origin)
	return &legacyAuthSource{Path: path, Key: key, Token: token, Data: data, Label: "credentials file"}, origin.BaseURL, nil
}

// applyLegacyProfileCredentials runs before applyStoredCredentials for an explicit profile: the earlier entry read
// the profile's own credentials file before the global one, so a legacy key there wins over a global credential.
func (c *Config) applyLegacyProfileCredentials(explicit bool, configPath string) error {
	if !explicit || c.hasCompleteCredentialFields() || c.credentialRouteBlocked() || c.AgentcookieManagedByExternalStore() {
		return nil
	}
	path, err := cliutil.CredentialsFilePathForConfig(configPath)
	if err != nil {
		return nil
	}
	src, _, err := readLegacyCredentialsFile(path)
	if err != nil || src == nil {
		return err
	}
	c.ConduytCrmBearerAuth = src.Token
	c.legacyAuth = src
	c.AuthSource = "config"
	c.CredentialSource = "credentials file"
	return nil
}

// applyLegacyGlobalCredentials runs after applyStoredCredentials: when nothing supplied a credential, a legacy key in
// the global credentials file does, with the same global-origin check an explicit profile applies to the global
// file (a mismatched origin is a recorded refusal, never an applied token).
func (c *Config) applyLegacyGlobalCredentials(explicit bool) error {
	if c.hasCompleteCredentialFields() || c.credentialRouteBlocked() || c.AgentcookieManagedByExternalStore() {
		return nil
	}
	path, err := cliutil.CredentialsFilePath()
	if err != nil {
		return nil
	}
	src, baseURL, err := readLegacyCredentialsFile(path)
	if err != nil || src == nil {
		return err
	}
	creds := &cliutil.Credentials{BaseURL: baseURL, ConduytCrmBearerAuth: src.Token}
	if explicit && !c.allowsGlobalCredential(creds) {
		c.addCredentialRefusal(cliutil.CredentialRefusal{
			Source:             "credentials file",
			Path:               path,
			Err:                c.globalCredentialOriginError(creds),
			CredentialsPresent: true,
		})
		return nil
	}
	c.ConduytCrmBearerAuth = src.Token
	c.legacyAuth = src
	c.AuthSource = "config"
	c.CredentialSource = "credentials file"
	return nil
}

// finishLegacyAuth runs at the end of Load. When a legacy key supplied the token that is still in effect, it notes
// the source and, where supported, migrates that one file.
func (c *Config) finishLegacyAuth() {
	src := c.legacyAuth
	if src == nil || c.ConduytCrmBearerAuth != src.Token {
		c.legacyAuth = nil
		return
	}
	if !legacyAuthRewriteSupported() {
		legacyAuthNotice("in-place:"+src.Path, "%s in %s is the earlier entry's key; it is read in place on this platform. Run 'conduyt-crm-pp-cli auth set-token' to save it as %s", src.Key, src.Path, currentAuthFileKey)
		return
	}
	backup, err := migrateLegacyAuthFile(src)
	if err != nil {
		legacyAuthNotice("migrate-failed:"+src.Path, "the saved %s in %s was used but not migrated to %s (%v)", src.Key, src.Path, currentAuthFileKey, err)
		return
	}
	if backup != "" {
		legacyAuthNotice("migrated:"+src.Path, "migrated the saved %s in %s to %s (backup: %s)", src.Key, src.Path, currentAuthFileKey, backup)
	}
}

var errLegacyAuthChanged = errors.New("the file changed since it was read; left as is")

// migrateLegacyAuthFile rewrites src.Path so its legacy key becomes crm_bearer_auth. It holds the credentials-file
// lock (the one the CLI's own credential saves take) and the file's own lock, re-reads the file and proceeds only if
// it still holds exactly the bytes the load used. It returns the backup path, or "" when there was nothing to do.
func migrateLegacyAuthFile(src *legacyAuthSource) (string, error) {
	globalCreds, err := cliutil.CredentialsFilePath()
	if err != nil {
		return "", err
	}
	// The lock file lives beside the global credentials file, as for the CLI's own credential saves.
	if err := os.MkdirAll(filepath.Dir(globalCreds), 0o700); err != nil {
		return "", err
	}
	var backup string
	run := func() error {
		var inner error
		backup, inner = migrateLegacyAuthFileLocked(src)
		return inner
	}
	if filepath.Clean(globalCreds) == filepath.Clean(src.Path) {
		return backup, cliutil.WithFileLock(src.Path, run)
	}
	err = cliutil.WithFileLock(globalCreds, func() error { return cliutil.WithFileLock(src.Path, run) })
	return backup, err
}

func migrateLegacyAuthFileLocked(src *legacyAuthSource) (string, error) {
	current, err := os.ReadFile(src.Path)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(current, src.Data) {
		return "", errLegacyAuthChanged
	}
	if err := cliutil.VerifyCredsPerms(src.Path); err != nil {
		return "", fmt.Errorf("the file is no longer owner-only: %w", err)
	}
	doc := map[string]any{}
	if err := toml.Unmarshal(current, &doc); err != nil {
		return "", fmt.Errorf("parsing: %w", err)
	}
	doc[currentAuthFileKey] = src.Token
	for _, k := range legacyAuthFileKeys {
		delete(doc, k)
	}
	out, err := toml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("encoding: %w", err)
	}
	backup, err := writeLegacyAuthBackup(src.Path, current)
	if err != nil {
		return "", err
	}
	if err := replacePrivateFileVerified(src.Path, out); err != nil {
		return backup, err
	}
	return backup, nil
}

// writeLegacyAuthBackup creates a new, collision-free backup beside path (O_EXCL, owner-only), writes the original
// bytes, and verifies the landed file is owner-only and byte-identical. It never reuses an existing file.
func writeLegacyAuthBackup(path string, data []byte) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		name := fmt.Sprintf("%s.legacy-auth-%d-%d.bak", path, time.Now().UnixNano(), os.Getpid())
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("creating backup: %w", err)
		}
		_, werr := f.Write(data)
		serr := f.Sync()
		cerr := f.Close()
		if err := errors.Join(werr, serr, cerr); err != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("writing backup: %w", err)
		}
		if err := verifyLandedPrivateFile(name, data); err != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("backup: %w", err)
		}
		return name, nil
	}
	return "", errors.New("could not create a unique backup name")
}

// replacePrivateFileVerified writes data to a new owner-only temp file beside path, verifies it, and only then renames
// it over path, so a file that would land with broader access never replaces the original.
func replacePrivateFileVerified(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".legacy-auth-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("securing temp file: %w", err)
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		cleanup()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := verifyLandedPrivateFile(tmp, data); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return fmt.Errorf("replacing: %w", err)
	}
	return verifyLandedPrivateFile(path, data)
}

func verifyLandedPrivateFile(path string, want []byte) error {
	if err := cliutil.VerifyCredsPerms(path); err != nil {
		return fmt.Errorf("%s is not owner-only: %w", filepath.Base(path), err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s does not hold the bytes written", filepath.Base(path))
	}
	return nil
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
