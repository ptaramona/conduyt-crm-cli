// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

package config

// PATCH(novel1219-auth-compat): env precedence; the earlier entry's saved keys read through the existing
// checked-descriptor loaders as extra field names of the same documents (same trust, refusal and fallback rules as
// crm_bearer_auth); nothing rewritten; no secret in any output. Greptile PR #2450 round 2 P1, Codex novel1219-r6/r7.

import (
	"bytes"
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
	headerToken  = "Bearer AUTH-HEADER-SECRET-4444"
	defaultBase  = "https://conduyt.app/api/v1"
)

// isolateLegacyAuth isolates every credential source and captures the notices.
func isolateLegacyAuth(t *testing.T) *bytes.Buffer {
	t.Helper()
	isolateCredentials(t)
	t.Setenv("CONDUYT_BEARER_AUTH", "")
	t.Setenv("CONDUYT_API_KEY", "")
	var buf bytes.Buffer
	prev := legacyAuthNoticeWriter
	legacyAuthNoticeWriter = &buf
	legacyAuthNoticeOnce = sync.Map{}
	t.Cleanup(func() {
		legacyAuthNoticeWriter = prev
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
	for _, s := range []string{legacyTokenA, legacyTokenB, currentToken, headerToken} {
		if strings.Contains(text, s) {
			t.Fatalf("%s leaks a secret value: %q", where, text)
		}
	}
}

func assertUnchanged(t *testing.T, path, body string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != body {
		t.Fatalf("%s was rewritten (err %v)", filepath.Base(path), err)
	}
	if extra, _ := filepath.Glob(path + ".*"); len(extra) != 0 {
		t.Fatalf("files written beside %s: %v", filepath.Base(path), extra)
	}
}

func profilePaths(t *testing.T) (configPath, credsPath string) {
	t.Helper()
	configPath = filepath.Join(t.TempDir(), "profile", "config.toml")
	credsPath, err := cliutil.CredentialsFilePathForConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return configPath, credsPath
}

func globalCredsPath(t *testing.T) string {
	t.Helper()
	p, err := cliutil.CredentialsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	return p
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
		// Codex r8 P1 (env): a set-but-blank name is an explicit clear and never falls through to a later name.
		{"blank current blocks legacy", blankEnv, legacyTokenA, legacyTokenB, "", "", ""},
		{"blank bearer auth blocks api key", "", blankEnv, legacyTokenB, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notices := isolateLegacyAuth(t)
			setOrUnset(t, "CONDUYT_CRM_BEARER_AUTH", tc.crm)
			setOrUnset(t, "CONDUYT_BEARER_AUTH", tc.bearer)
			setOrUnset(t, "CONDUYT_API_KEY", tc.apiKy)
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

// Saved earlier keys in the config file are honoured in place, never rewritten, with one notice naming the file and
// the re-save command; a document with crm_bearer_auth uses it only; repeated loads behave the same.
func TestLegacyAuthConfigKeysReadInPlace(t *testing.T) {
	for _, tc := range []struct {
		name, body, want, wantKey string
	}{
		{"bearer_auth", "base_url = \"" + defaultBase + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", legacyTokenA, "bearer_auth"},
		{"api_key", "api_key = \"" + legacyTokenB + "\"\n", legacyTokenB, "api_key"},
		{"both prefer bearer_auth", "api_key = \"" + legacyTokenB + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", legacyTokenA, "bearer_auth"},
		{"current key wins", "crm_bearer_auth = \"" + currentToken + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", currentToken, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notices := isolateLegacyAuth(t)
			path := filepath.Join(t.TempDir(), "config.toml")
			writeOwnerOnly(t, path, tc.body)
			for run := 0; run < 2; run++ {
				legacyAuthNoticeOnce = sync.Map{}
				notices.Reset()
				cfg, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.ConduytCrmBearerAuth != tc.want || cfg.AuthHeader() != "Bearer "+tc.want || cfg.CredentialSource != "config-kind path" {
					t.Fatalf("run %d: saved credential not honoured with its provenance (%q / %q)", run, cfg.AuthSource, cfg.CredentialSource)
				}
				if tc.wantKey == "" && notices.Len() != 0 || tc.wantKey != "" && (!strings.Contains(notices.String(), path) || !strings.Contains(notices.String(), "earlier key "+tc.wantKey) || !strings.Contains(notices.String(), legacyResaveCommand)) {
					t.Fatalf("run %d: notices = %q", run, notices.String())
				}
				assertNoSecret(t, "notice", notices.String())
				assertUnchanged(t, path, tc.body)
			}
		})
	}
}

// Legacy keys in a profile's own credentials file and in the global credentials file are read by the existing
// loader, in the earlier entry's precedence (profile first) and with the global-origin check.
func TestLegacyAuthCredentialsFilesReadInPlace(t *testing.T) {
	t.Run("profile file", func(t *testing.T) {
		notices := isolateLegacyAuth(t)
		configPath, credsPath := profilePaths(t)
		body := "base_url = \"" + defaultBase + "\"\napi_key = \"" + legacyTokenB + "\"\n"
		writeOwnerOnly(t, credsPath, body)
		writeOwnerOnly(t, globalCredsPath(t), "base_url = \""+defaultBase+"\"\ncrm_bearer_auth = \""+currentToken+"\"\n")
		cfg, err := Load(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != legacyTokenB || cfg.CredentialSource != "credentials file" || !strings.Contains(notices.String(), credsPath) {
			t.Fatalf("profile legacy credential: honoured=%t source=%q notices=%q", cfg.ConduytCrmBearerAuth == legacyTokenB, cfg.CredentialSource, notices.String())
		}
		assertUnchanged(t, credsPath, body)
		assertNoSecret(t, "notice", notices.String())
	})
	t.Run("global file, matching origin", func(t *testing.T) {
		isolateLegacyAuth(t)
		configPath, _ := profilePaths(t)
		body := "base_url = \"" + defaultBase + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n"
		writeOwnerOnly(t, globalCredsPath(t), body)
		cfg, err := Load(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != legacyTokenA {
			t.Fatalf("global legacy credential not honoured (refusals %v)", cfg.CredentialRefusalSummaries())
		}
		assertUnchanged(t, globalCredsPath(t), body)
	})
	t.Run("global file, other origin", func(t *testing.T) {
		isolateLegacyAuth(t)
		configPath, _ := profilePaths(t)
		writeOwnerOnly(t, globalCredsPath(t), "base_url = \"https://other.example.com/api/v1\"\nbearer_auth = \""+legacyTokenA+"\"\n")
		cfg, err := Load(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != "" || !cfg.HasCredentialRefusals() {
			t.Fatal("a global legacy credential for another origin was applied")
		}
		for _, s := range cfg.CredentialRefusalSummaries() {
			assertNoSecret(t, "refusal", s)
		}
	})
}

// Codex r7 P1 #2: an unsafe profile credentials file holding only an earlier key is a present refusal and suppresses
// the global fallback (current or legacy key), exactly as the same file with crm_bearer_auth does.
func TestLegacyAuthUnsafeProfileFileSuppressesGlobalFallback(t *testing.T) {
	for _, profileKey := range []string{"bearer_auth", "api_key", "crm_bearer_auth"} {
		for _, globalKey := range []string{"crm_bearer_auth", "bearer_auth"} {
			t.Run(profileKey+"/"+globalKey, func(t *testing.T) {
				isolateLegacyAuth(t)
				configPath, credsPath := profilePaths(t)
				writeOwnerOnly(t, credsPath, "base_url = \""+defaultBase+"\"\n"+profileKey+" = \""+legacyTokenA+"\"\n")
				if err := os.Chmod(credsPath, 0o644); err != nil {
					t.Fatal(err)
				}
				writeOwnerOnly(t, globalCredsPath(t), "base_url = \""+defaultBase+"\"\n"+globalKey+" = \""+currentToken+"\"\n")
				cfg, err := Load(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.ConduytCrmBearerAuth != "" || !cfg.HasCredentialRefusals() {
					t.Fatalf("unsafe %s profile file: token applied=%t refusals=%v", profileKey, cfg.ConduytCrmBearerAuth != "", cfg.CredentialRefusalSummaries())
				}
				for _, s := range cfg.CredentialRefusalSummaries() {
					assertNoSecret(t, "refusal", s)
				}
			})
		}
	}
}

// Codex r7 P2: a profile document with auth_header and an earlier key keeps the normal field precedence (auth_header
// wins), and the same request goes out before and after the user re-saves the token under crm_bearer_auth.
func TestLegacyAuthMixedDocumentKeepsFieldPrecedence(t *testing.T) {
	isolateLegacyAuth(t)
	configPath, credsPath := profilePaths(t)
	writeOwnerOnly(t, credsPath, "base_url = \""+defaultBase+"\"\nauth_header = \""+headerToken+"\"\nbearer_auth = \""+legacyTokenA+"\"\n")
	before, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	writeOwnerOnly(t, credsPath, "base_url = \""+defaultBase+"\"\nauth_header = \""+headerToken+"\"\ncrm_bearer_auth = \""+legacyTokenA+"\"\n")
	after, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.AuthHeader() != headerToken || after.AuthHeader() != headerToken {
		t.Fatal("a mixed document does not keep auth_header ahead of the bearer token, before and after a re-save")
	}
	writeOwnerOnly(t, credsPath, "base_url = \""+defaultBase+"\"\ncrm_bearer_auth = \""+currentToken+"\"\nbearer_auth = \""+legacyTokenA+"\"\n")
	both, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if both.ConduytCrmBearerAuth != currentToken {
		t.Fatal("a document with crm_bearer_auth and bearer_auth did not use crm_bearer_auth")
	}
}

// Codex r7 P1 #1: legacy keys are read only through the existing checked-descriptor loaders, so every trust rule
// those loaders apply to crm_bearer_auth applies to bearer_auth / api_key with the identical outcome: a symlinked
// profile data directory and a loose file are refused for both. legacy_auth.go holds no file read at all.
func TestLegacyAuthTrustOutcomesMatchTheCurrentKey(t *testing.T) {
	cases := map[string]func(t *testing.T, configPath, credsPath, body string){
		"loose mode": func(t *testing.T, _, credsPath, body string) {
			writeOwnerOnly(t, credsPath, body)
			if err := os.Chmod(credsPath, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked profile data directory": func(t *testing.T, _, credsPath, body string) {
			elsewhere := filepath.Join(t.TempDir(), "data")
			writeOwnerOnly(t, filepath.Join(elsewhere, filepath.Base(credsPath)), body)
			if err := os.MkdirAll(filepath.Dir(filepath.Dir(credsPath)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Dir(credsPath)); err != nil {
				t.Fatal(err)
			}
		},
		"owner-only regular file": func(t *testing.T, _, credsPath, body string) {
			writeOwnerOnly(t, credsPath, body)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			outcome := map[string]string{}
			for _, key := range []string{"crm_bearer_auth", "bearer_auth", "api_key"} {
				isolateLegacyAuth(t)
				configPath, credsPath := profilePaths(t)
				setup(t, configPath, credsPath, "base_url = \""+defaultBase+"\"\n"+key+" = \""+legacyTokenA+"\"\n")
				cfg, err := Load(configPath)
				result := "error"
				if err == nil {
					result = "applied=" + boolWord(cfg.ConduytCrmBearerAuth == legacyTokenA) + " refused=" + boolWord(cfg.HasCredentialRefusals())
				}
				outcome[key] = result
			}
			if outcome["bearer_auth"] != outcome["crm_bearer_auth"] || outcome["api_key"] != outcome["crm_bearer_auth"] {
				t.Fatalf("%s: legacy keys diverge from the current key: %v", name, outcome)
			}
		})
	}
	src, err := os.ReadFile("legacy_auth.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"os.ReadFile", "os.Open", "os.Lstat", "os.Stat", "os.WriteFile", "os.Rename", "os.CreateTemp"} {
		if strings.Contains(string(src), call+"(") {
			t.Fatalf("legacy_auth.go calls %s: legacy keys must be read only through the checked-descriptor loaders", call)
		}
	}
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func TestLegacyAuthUntrustedConfigFileIsRefused(t *testing.T) {
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
	if cfg.ConduytCrmBearerAuth != "" || !cfg.HasCredentialRefusals() {
		t.Fatalf("an untrusted config file's legacy key was used (refusals %v)", cfg.CredentialRefusalSummaries())
	}
	assertUnchanged(t, path, body)
}

func TestLegacyAuthSavesNeverWriteLegacyKeys(t *testing.T) {
	isolateLegacyAuth(t)
	if err := cliutil.SaveCredentials(&cliutil.Credentials{BaseURL: defaultBase, ConduytCrmBearerAuth: currentToken, LegacyBearerAuth: legacyTokenA, LegacyKey: "bearer_auth"}); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(globalCredsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConduytCrmBearerAuth != currentToken {
		t.Fatal("saved current key not loaded")
	}
	if strings.Contains(string(persisted), "bearer_auth =") && !strings.Contains(string(persisted), "crm_bearer_auth") || strings.Contains(strings.ReplaceAll(string(persisted), "crm_bearer_auth", ""), "bearer_auth") || strings.Contains(string(persisted), "api_key") || strings.Contains(string(persisted), legacyTokenA) || strings.Contains(string(persisted), "LegacyKey") {
		t.Fatalf("internal provenance fields were persisted:\n%s", persisted)
	}
}

// blankEnv marks an env var that is set to the empty string (as opposed to "", unset).
const blankEnv = "<blank>"

func setOrUnset(t *testing.T, name, value string) {
	t.Helper()
	t.Setenv(name, "")
	switch value {
	case "":
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	case blankEnv:
		// stays set to ""
	default:
		t.Setenv(name, value)
	}
}

// ---- Codex novel1219-r8 regressions (r9) ----

// Codex r8 P1 #1: a document that has crm_bearer_auth at all, even empty (an explicitly cleared token), ignores the
// earlier keys: config document, profile credentials file and global credentials file alike.
func TestLegacyAuthEmptyCurrentKeyNeverResurrectsALegacyKey(t *testing.T) {
	cleared := "base_url = \"" + defaultBase + "\"\ncrm_bearer_auth = \"\"\nbearer_auth = \"" + legacyTokenA + "\"\napi_key = \"" + legacyTokenB + "\"\n"
	t.Run("config document", func(t *testing.T) {
		isolateLegacyAuth(t)
		path := filepath.Join(t.TempDir(), "config.toml")
		writeOwnerOnly(t, path, cleared)
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != "" || cfg.AuthHeader() != "" {
			t.Fatal("an empty crm_bearer_auth in the config document resurrected an earlier key")
		}
	})
	t.Run("profile credentials file", func(t *testing.T) {
		isolateLegacyAuth(t)
		configPath, credsPath := profilePaths(t)
		writeOwnerOnly(t, credsPath, cleared)
		cfg, err := Load(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != "" || cfg.AuthHeader() != "" {
			t.Fatal("an empty crm_bearer_auth in the profile credentials file resurrected an earlier key")
		}
	})
	t.Run("global credentials file", func(t *testing.T) {
		isolateLegacyAuth(t)
		writeOwnerOnly(t, globalCredsPath(t), cleared)
		cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConduytCrmBearerAuth != "" || cfg.AuthHeader() != "" {
			t.Fatal("an empty crm_bearer_auth in the global credentials file resurrected an earlier key")
		}
	})
}

// Codex r9 P1 #1 / r8 P1 #2 (re-save): auth set-token saves crm_bearer_auth, the top bearer key, into the global file
// and into the explicit profile's existing credentials file; the old profile key stays in the file but is unreachable,
// and the new token is sent, also with an env overlay at save time and with a mixed profile document.
func TestLegacyAuthReSaveSendsTheNewToken(t *testing.T) {
	const rotated = "ROTATED-SET-TOKEN-SECRET-5555"
	for _, tc := range []struct{ name, profile, envOverlay string }{
		{"legacy profile key", "base_url = \"" + defaultBase + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", ""},
		{"legacy profile key under an env overlay", "base_url = \"" + defaultBase + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", currentToken},
		{"mixed profile with an older access token", "base_url = \"" + defaultBase + "\"\naccess_token = \"OLD-ACCESS-SECRET-7777\"\napi_key = \"" + legacyTokenB + "\"\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLegacyAuth(t)
			configPath, credsPath := profilePaths(t)
			writeOwnerOnly(t, credsPath, tc.profile)
			writeOwnerOnly(t, globalCredsPath(t), "base_url = \""+defaultBase+"\"\napi_key = \""+legacyTokenB+"\"\n")
			if tc.envOverlay != "" {
				t.Setenv("CONDUYT_CRM_BEARER_AUTH", tc.envOverlay)
			}
			cfg, err := Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.AuthHeaderVal = ""
			if err := cfg.SaveBearerToken(rotated); err != nil { // what auth set-token does
				t.Fatal(err)
			}
			setOrUnset(t, "CONDUYT_CRM_BEARER_AUTH", "")
			after, err := Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if after.AuthHeader() != "Bearer "+rotated {
				t.Fatal("after the re-save the new token is not the one sent")
			}
			raw, _ := os.ReadFile(credsPath)
			if !strings.Contains(string(raw), rotated) {
				t.Fatal("the profile credentials file was not given crm_bearer_auth")
			}
			for _, key := range []string{"bearer_auth = ", "api_key = ", "access_token = "} {
				if strings.Contains(tc.profile, "\n"+key) && !strings.Contains(string(raw), key) {
					t.Fatalf("the re-save removed %q from the profile file; only crm_bearer_auth may change", key)
				}
			}
		})
	}
}

// Codex r8 P1 #2 (logout): logout writes crm_bearer_auth = "" (explicitly cleared) into the explicit profile's file;
// the old keys stay in it but are unreachable, so the next load finds no credential. No file is deleted.
func TestLegacyAuthLogoutClearsWithoutDeleting(t *testing.T) {
	isolateLegacyAuth(t)
	configPath, credsPath := profilePaths(t)
	writeOwnerOnly(t, credsPath, "base_url = \""+defaultBase+"\"\nbearer_auth = \""+legacyTokenA+"\"\napi_key = \""+legacyTokenB+"\"\n")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ClearTokens(); err != nil {
		t.Fatal(err)
	}
	after, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.AuthHeader() != "" || after.ConduytCrmBearerAuth != "" {
		t.Fatal("a credential survived logout")
	}
	raw, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("the profile credentials file was deleted: %v", err)
	}
	doc := string(raw)
	if !strings.Contains(doc, "crm_bearer_auth = ''") && !strings.Contains(doc, "crm_bearer_auth = \"\"") || !strings.Contains(doc, legacyTokenA) || !strings.Contains(doc, legacyTokenB) {
		t.Fatal("logout must only set crm_bearer_auth to \"\" and leave the rest of the file as it is")
	}
	if err := cliutil.VerifyCredsPerms(credsPath); err != nil {
		t.Fatalf("rewritten profile file is not owner-only: %v", err)
	}
}

// Logout never follows a symlinked profile data directory (the loader refuses it too), and leaves an unsafe profile
// file alone (the loader refuses it and suppresses the global fallback).
func TestLegacyAuthLogoutDoesNotFollowASymlinkedProfileDataDir(t *testing.T) {
	isolateLegacyAuth(t)
	configPath, credsPath := profilePaths(t)
	elsewhere := filepath.Join(t.TempDir(), "data")
	target := filepath.Join(elsewhere, filepath.Base(credsPath))
	body := "base_url = \"" + defaultBase + "\"\ncrm_bearer_auth = \"" + currentToken + "\"\n"
	writeOwnerOnly(t, target, body)
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(credsPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Dir(credsPath)); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ClearTokens(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != body {
		t.Fatal("logout wrote or removed a file through a symlinked profile data directory")
	}
}

// The auth-compat code has no file deletion path: no os.Remove / RemoveAll anywhere in it, and the profile writer's
// only unlink is of its own temp file.
func TestLegacyAuthHasNoFileDeletionPath(t *testing.T) {
	for _, f := range []string{"legacy_auth.go", "../cliutil/creds_profile_write_unix.go", "../cliutil/creds_profile_write_windows.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if strings.Contains(text, "os.Remove(") || strings.Contains(text, "os.RemoveAll(") || strings.Contains(text, "RemoveCredentials(") {
			t.Fatalf("%s has a file deletion path", f)
		}
		if n := strings.Count(text, "Unlinkat("); n != strings.Count(text, "Unlinkat(dirfd, tmpName, 0)") {
			t.Fatalf("%s unlinks something other than its own temp file", f)
		}
	}
}
