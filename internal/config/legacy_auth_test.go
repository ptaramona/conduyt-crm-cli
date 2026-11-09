// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

package config

// PATCH(novel1219-auth-compat): env precedence; legacy keys read in place through the normal sources, order and
// trust rules; one-source migration (lock + compare, verified owner-only files, unique backup); Windows reads in
// place; no secret in any notice. Greptile PR #2450 round 2 P1 and Codex novel1219-r6.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ptaramona/conduyt-crm-cli/internal/cliutil"
)

const (
	legacyTokenA = "LEGACY-BEARER-AUTH-SECRET-1111"
	legacyTokenB = "LEGACY-API-KEY-SECRET-2222"
	currentToken = "CURRENT-CRM-BEARER-SECRET-3333"
	defaultBase  = "https://conduyt.app/api/v1"
)

// isolateLegacyAuth isolates every credential source and captures the notices.
func isolateLegacyAuth(t *testing.T) *bytes.Buffer {
	t.Helper()
	isolateCredentials(t)
	t.Setenv("CONDUYT_BEARER_AUTH", "")
	t.Setenv("CONDUYT_API_KEY", "")
	var buf bytes.Buffer
	prevW, prevR := legacyAuthNoticeWriter, legacyAuthRewriteSupported
	legacyAuthNoticeWriter = &buf
	legacyAuthNoticeOnce = sync.Map{}
	t.Cleanup(func() {
		legacyAuthNoticeWriter, legacyAuthRewriteSupported = prevW, prevR
		legacyAuthNoticeOnce = sync.Map{}
	})
	return &buf
}

func writeOwnerOnly(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoSecret(t *testing.T, where, text string) {
	t.Helper()
	for _, s := range []string{legacyTokenA, legacyTokenB, currentToken} {
		if strings.Contains(text, s) {
			t.Fatalf("%s leaks a secret value: %q", where, text)
		}
	}
}

func backupsOf(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".legacy-auth-*.bak")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestLegacyAuthEnvPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name               string
		crm, bearer, apiKy string
		want, wantSource   string
		wantNotice         string
	}{
		{"current only", currentToken, "", "", currentToken, "env:CONDUYT_CRM_BEARER_AUTH", ""},
		{"current beats legacy", currentToken, legacyTokenA, legacyTokenB, currentToken, "env:CONDUYT_CRM_BEARER_AUTH", ""},
		{"bearer auth fallback", "", legacyTokenA, "", legacyTokenA, "env:CONDUYT_BEARER_AUTH", "CONDUYT_BEARER_AUTH is deprecated"},
		{"bearer auth beats api key", "", legacyTokenA, legacyTokenB, legacyTokenA, "env:CONDUYT_BEARER_AUTH", "CONDUYT_BEARER_AUTH is deprecated"},
		{"api key fallback", "", "", legacyTokenB, legacyTokenB, "env:CONDUYT_API_KEY", "CONDUYT_API_KEY is deprecated"},
		{"none", "", "", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notices := isolateLegacyAuth(t)
			t.Setenv("CONDUYT_CRM_BEARER_AUTH", tc.crm)
			t.Setenv("CONDUYT_BEARER_AUTH", tc.bearer)
			t.Setenv("CONDUYT_API_KEY", tc.apiKy)
			cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ConduytCrmBearerAuth != tc.want || cfg.AuthSource != tc.wantSource {
				t.Fatalf("token match=%t source=%q, want source %q", cfg.ConduytCrmBearerAuth == tc.want, cfg.AuthSource, tc.wantSource)
			}
			if tc.want != "" && cfg.AuthHeader() != "Bearer "+tc.want {
				t.Fatal("AuthHeader does not carry the selected token")
			}
			if tc.wantNotice == "" && notices.Len() != 0 || tc.wantNotice != "" && strings.Count(notices.String(), "\n") != 1 || !strings.Contains(notices.String(), tc.wantNotice) {
				t.Fatalf("notices = %q, want %q once", notices.String(), tc.wantNotice)
			}
			assertNoSecret(t, "notice", notices.String())
		})
	}
}

func TestLegacyAuthConfigMigratesOnceAndIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name, body, want, wantKey string
		migrates                  bool
	}{
		{"bearer_auth", "base_url = \"" + defaultBase + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", legacyTokenA, "bearer_auth", true},
		{"api_key", "api_key = \"" + legacyTokenB + "\"\n", legacyTokenB, "api_key", true},
		{"both prefer bearer_auth", "api_key = \"" + legacyTokenB + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", legacyTokenA, "bearer_auth", true},
		{"current already set wins and the file is left alone", "crm_bearer_auth = \"" + currentToken + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", currentToken, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notices := isolateLegacyAuth(t)
			path := filepath.Join(t.TempDir(), "config.toml")
			writeOwnerOnly(t, path, tc.body)
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ConduytCrmBearerAuth != tc.want || cfg.AuthHeader() != "Bearer "+tc.want || cfg.CredentialSource != "config-kind path" {
				t.Fatalf("legacy credential not honoured with its provenance (source %q / %q)", cfg.AuthSource, cfg.CredentialSource)
			}
			after, _ := os.ReadFile(path)
			if !tc.migrates {
				if string(after) != tc.body || len(backupsOf(t, path)) != 0 || notices.Len() != 0 {
					t.Fatalf("a file whose current key wins was rewritten or noticed: %q", notices.String())
				}
				return
			}
			doc := string(after)
			if strings.Contains(doc, "api_key") || strings.Contains(strings.ReplaceAll(doc, "crm_bearer_auth", ""), "bearer_auth") || !strings.Contains(doc, tc.want) {
				t.Fatalf("migration result wrong (legacy keys left or token missing)")
			}
			if err := cliutil.VerifyCredsPerms(path); err != nil {
				t.Fatalf("rewritten file is not owner-only: %v", err)
			}
			backups := backupsOf(t, path)
			if len(backups) != 1 {
				t.Fatalf("backups = %v, want exactly one", backups)
			}
			if raw, _ := os.ReadFile(backups[0]); string(raw) != tc.body || cliutil.VerifyCredsPerms(backups[0]) != nil {
				t.Fatal("backup is not the owner-only original")
			}
			if !strings.Contains(notices.String(), "migrated the saved "+tc.wantKey) {
				t.Fatalf("notices = %q", notices.String())
			}
			assertNoSecret(t, "notice", notices.String())

			// Idempotence: a second load (a new process) changes nothing and prints nothing.
			legacyAuthNoticeOnce = sync.Map{}
			notices.Reset()
			cfg2, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := os.ReadFile(path)
			if cfg2.ConduytCrmBearerAuth != tc.want || string(again) != string(after) || notices.Len() != 0 || len(backupsOf(t, path)) != 1 {
				t.Fatalf("second load changed state: same=%t notices=%q", string(again) == string(after), notices.String())
			}
		})
	}
}

// Codex r6 P1 #1: an explicit profile still falls back to the global credentials file, as the earlier entry did,
// so a legacy key there is honoured when its origin matches, refused (never applied, never migrated) when it does
// not, and a legacy key in the profile's own credentials file wins over a global credential.
func TestLegacyAuthExplicitProfileGlobalCredentials(t *testing.T) {
	t.Run("matching origin is honoured and migrated", func(t *testing.T) {
		notices := isolateLegacyAuth(t)
		globalPath, err := cliutil.CredentialsFilePath()
		if err != nil {
			t.Fatal(err)
		}
		writeOwnerOnly(t, globalPath, "base_url = \""+defaultBase+"\"\nbearer_auth = \""+legacyTokenA+"\"\n")
		cfg, err := Load(filepath.Join(t.TempDir(), "profile", "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != legacyTokenA || cfg.CredentialSource != "credentials file" {
			t.Fatalf("global legacy credential not honoured (source %q, refusals %v)", cfg.CredentialSource, cfg.CredentialRefusalSummaries())
		}
		if raw, _ := os.ReadFile(globalPath); strings.Contains(string(raw), "\nbearer_auth") || !strings.Contains(string(raw), "crm_bearer_auth") {
			t.Fatal("global credentials file not migrated")
		}
		assertNoSecret(t, "notice", notices.String())
	})
	t.Run("mismatched origin is refused", func(t *testing.T) {
		isolateLegacyAuth(t)
		globalPath, err := cliutil.CredentialsFilePath()
		if err != nil {
			t.Fatal(err)
		}
		body := "base_url = \"https://other.example.com/api/v1\"\nbearer_auth = \"" + legacyTokenA + "\"\n"
		writeOwnerOnly(t, globalPath, body)
		cfg, err := Load(filepath.Join(t.TempDir(), "profile", "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != "" || !cfg.HasCredentialRefusals() {
			t.Fatalf("a global legacy credential for another origin was applied (refusals %v)", cfg.CredentialRefusalSummaries())
		}
		if raw, _ := os.ReadFile(globalPath); string(raw) != body || len(backupsOf(t, globalPath)) != 0 {
			t.Fatal("a refused global credentials file was rewritten")
		}
		for _, s := range cfg.CredentialRefusalSummaries() {
			assertNoSecret(t, "refusal", s)
		}
	})
	t.Run("profile legacy key wins over a global credential", func(t *testing.T) {
		isolateLegacyAuth(t)
		globalPath, err := cliutil.CredentialsFilePath()
		if err != nil {
			t.Fatal(err)
		}
		writeOwnerOnly(t, globalPath, "base_url = \""+defaultBase+"\"\ncrm_bearer_auth = \""+currentToken+"\"\n")
		profile := filepath.Join(t.TempDir(), "profile", "config.toml")
		profileCreds, err := cliutil.CredentialsFilePathForConfig(profile)
		if err != nil {
			t.Fatal(err)
		}
		writeOwnerOnly(t, profileCreds, "base_url = \""+defaultBase+"\"\napi_key = \""+legacyTokenB+"\"\n")
		cfg, err := Load(profile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != legacyTokenB {
			t.Fatal("the profile's own legacy credential did not win over the global credential")
		}
	})
}

// Codex r6 P1 #2: where an owner-only rewrite cannot be guaranteed (Windows: chmod does not set a private DACL), the
// legacy key is read in place and the file is never rewritten; where it is, every landed file is verified owner-only.
func TestLegacyAuthWindowsReadsInPlace(t *testing.T) {
	notices := isolateLegacyAuth(t)
	legacyAuthRewriteSupported = func() bool { return false }
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "bearer_auth = \"" + legacyTokenA + "\"\n"
	writeOwnerOnly(t, path, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if cfg.ConduytCrmBearerAuth != legacyTokenA || string(after) != body || len(backupsOf(t, path)) != 0 || !strings.Contains(notices.String(), "read in place on this platform") {
		t.Fatalf("in-place read: honoured=%t unchanged=%t notices=%q", cfg.ConduytCrmBearerAuth == legacyTokenA, string(after) == body, notices.String())
	}
	assertNoSecret(t, "notice", notices.String())
}

func TestLegacyAuthLandedFilesAreVerifiedOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.toml")
	writeOwnerOnly(t, path, "old")
	if err := os.Chmod(dir, 0o777); err != nil { // a permissive parent must not widen the landed files
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := replacePrivateFileVerified(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	backup, err := writeLegacyAuthBackup(path, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, backup} {
		if err := cliutil.VerifyCredsPerms(p); err != nil {
			t.Fatalf("%s landed with broader access: %v", filepath.Base(p), err)
		}
	}
}

// Codex r6 P2 #1: the token keeps its provenance: a failed rewrite still reports the file that supplied it, and an
// inactive legacy config never supplies a token to an active primary config.
func TestLegacyAuthFailedRewriteKeepsProvenance(t *testing.T) {
	notices := isolateLegacyAuth(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeOwnerOnly(t, path, "bearer_auth = \""+legacyTokenA+"\"\n")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		_ = f.Close()
		t.Skip("directory stays writable for this user (running as root?)")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConduytCrmBearerAuth != legacyTokenA || cfg.AuthSource != "config" || cfg.CredentialSource != "config-kind path" || !strings.Contains(notices.String(), "was used but not migrated") {
		t.Fatalf("failed rewrite: honoured=%t source=%q/%q notices=%q", cfg.ConduytCrmBearerAuth == legacyTokenA, cfg.AuthSource, cfg.CredentialSource, notices.String())
	}
	assertNoSecret(t, "notice", notices.String())
}

func TestLegacyAuthInactiveLegacyConfigNeverSupplies(t *testing.T) {
	isolateLegacyAuth(t)
	primary, legacy := mustDefaultConfigPaths(t)
	writeOwnerOnly(t, primary, "base_url = \"https://other.example.com/api/v1\"\n")
	writeOwnerOnly(t, legacy, "bearer_auth = \""+legacyTokenA+"\"\n")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConduytCrmBearerAuth != "" {
		t.Fatal("an inactive legacy config supplied a token to the active primary config")
	}
}

func mustDefaultConfigPaths(t *testing.T) (string, string) {
	t.Helper()
	primary, _, err := resolveConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := LegacyConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if primary == legacy {
		t.Skip("primary and legacy config paths coincide in this environment")
	}
	return primary, legacy
}

// Codex r6 P2 #2: a migration never overwrites a file that changed after the load read it.
func TestLegacyAuthMigrationRefusesAChangedFile(t *testing.T) {
	isolateLegacyAuth(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "bearer_auth = \"" + legacyTokenA + "\"\n"
	writeOwnerOnly(t, path, original)
	rotated := "crm_bearer_auth = \"" + currentToken + "\"\n"
	writeOwnerOnly(t, path, rotated) // another process rotated the credential after this load read `original`
	_, err := migrateLegacyAuthFile(&legacyAuthSource{Path: path, Key: "bearer_auth", Token: legacyTokenA, Data: []byte(original), Label: "config"})
	if !errors.Is(err, errLegacyAuthChanged) {
		t.Fatalf("err = %v, want the changed-file refusal", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != rotated || len(backupsOf(t, path)) != 0 {
		t.Fatal("the rotated credential was overwritten")
	}
}

// Codex r6 P3: an existing file at any backup name is never reused; each migration writes a fresh, verified backup.
func TestLegacyAuthNeverReusesAnExistingBackup(t *testing.T) {
	notices := isolateLegacyAuth(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "bearer_auth = \"" + legacyTokenA + "\"\n"
	writeOwnerOnly(t, path, body)
	stale := path + ".legacy-auth.bak"
	writeOwnerOnly(t, stale, "stale")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), path+".legacy-auth-0-0.bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(stale); string(raw) != "stale" {
		t.Fatal("a pre-existing backup was overwritten")
	}
	var fresh []string
	for _, b := range backupsOf(t, path) {
		if info, err := os.Lstat(b); err == nil && info.Mode().IsRegular() {
			fresh = append(fresh, b)
		}
	}
	if len(fresh) != 1 {
		t.Fatalf("regular backups = %v, want one fresh backup", fresh)
	}
	if raw, _ := os.ReadFile(fresh[0]); string(raw) != body {
		t.Fatal("the fresh backup does not hold the original bytes")
	}
	assertNoSecret(t, "notice", notices.String())
}

func TestLegacyAuthNeverReadsOrRewritesAnUntrustedFile(t *testing.T) {
	isolateLegacyAuth(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "bearer_auth = \"" + legacyTokenA + "\"\n"
	writeOwnerOnly(t, path, body)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if cfg.ConduytCrmBearerAuth != "" || string(after) != body || len(backupsOf(t, path)) != 0 {
		t.Fatalf("an untrusted legacy file was used or rewritten (applied=%t)", cfg.ConduytCrmBearerAuth != "")
	}
}
