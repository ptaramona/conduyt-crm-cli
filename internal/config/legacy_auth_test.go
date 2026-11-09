// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

package config

// PATCH(novel1219-auth-compat): env precedence, legacy config / credentials migration (one-time, idempotent,
// backed up) and no secret in any notice. Greptile PR #2450 round 2 P1.

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
	for _, s := range []string{legacyTokenA, legacyTokenB, currentToken} {
		if strings.Contains(text, s) {
			t.Fatalf("%s leaks a secret value: %q", where, text)
		}
	}
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
		name, body, want, wantNotice string
	}{
		{"bearer_auth", "base_url = \"https://conduyt.app/api/v1\"\nbearer_auth = \"" + legacyTokenA + "\"\n", legacyTokenA, "migrated the saved bearer_auth"},
		{"api_key", "api_key = \"" + legacyTokenB + "\"\n", legacyTokenB, "migrated the saved api_key"},
		{"both prefer bearer_auth", "api_key = \"" + legacyTokenB + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", legacyTokenA, "migrated the saved bearer_auth"},
		{"current already set", "crm_bearer_auth = \"" + currentToken + "\"\nbearer_auth = \"" + legacyTokenA + "\"\n", currentToken, "removed the superseded legacy credential key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notices := isolateLegacyAuth(t)
			path := filepath.Join(t.TempDir(), "config.toml")
			writeOwnerOnly(t, path, tc.body)
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ConduytCrmBearerAuth != tc.want || cfg.AuthHeader() != "Bearer "+tc.want {
				t.Fatalf("legacy credential not honoured (source %q)", cfg.AuthSource)
			}
			after, _ := os.ReadFile(path)
			if strings.Contains(string(after), "bearer_auth =") && !strings.Contains(string(after), "crm_bearer_auth") || strings.Contains(string(after), "api_key") || strings.Contains(string(after), "\nbearer_auth") || strings.HasPrefix(string(after), "bearer_auth") {
				t.Fatalf("legacy keys remain after migration:\n%s", after)
			}
			if !strings.Contains(string(after), "crm_bearer_auth = '"+tc.want+"'") && !strings.Contains(string(after), "crm_bearer_auth = \""+tc.want+"\"") {
				t.Fatalf("crm_bearer_auth not written:\n%s", after)
			}
			if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
				t.Fatalf("rewritten file mode %v, want 0600", info.Mode().Perm())
			}
			backup, err := os.ReadFile(path + ".legacy-auth.bak")
			if err != nil || string(backup) != tc.body {
				t.Fatalf("backup missing or altered: %v", err)
			}
			if !strings.Contains(notices.String(), tc.wantNotice) {
				t.Fatalf("notices = %q, want %q", notices.String(), tc.wantNotice)
			}
			assertNoSecret(t, "notice", notices.String())

			// Idempotence: a second load (a new process) leaves the file and the backup alone and says nothing.
			legacyAuthNoticeOnce = sync.Map{}
			notices.Reset()
			cfg2, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := os.ReadFile(path)
			if cfg2.ConduytCrmBearerAuth != tc.want || string(again) != string(after) || notices.Len() != 0 {
				t.Fatalf("second load changed state: same=%t notices=%q", string(again) == string(after), notices.String())
			}
		})
	}
}

func TestLegacyAuthCredentialsFileMigrates(t *testing.T) {
	notices := isolateLegacyAuth(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeOwnerOnly(t, path, "base_url = \"https://conduyt.app/api/v1\"\n")
	credsPath, err := cliutil.CredentialsFilePathForConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	writeOwnerOnly(t, credsPath, "base_url = \"https://conduyt.app/api/v1\"\napi_key = \""+legacyTokenB+"\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConduytCrmBearerAuth != legacyTokenB {
		t.Fatalf("credentials-file legacy key not honoured (source %q, refusals %v)", cfg.AuthSource, cfg.CredentialRefusalSummaries())
	}
	after, _ := os.ReadFile(credsPath)
	if strings.Contains(string(after), "api_key") || !strings.Contains(string(after), "crm_bearer_auth") {
		t.Fatalf("credentials file not migrated:\n%s", after)
	}
	assertNoSecret(t, "notice", notices.String())
}

func TestLegacyAuthNeverRewritesAnUntrustedFile(t *testing.T) {
	isolateLegacyAuth(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "bearer_auth = \"" + legacyTokenA + "\"\n"
	writeOwnerOnly(t, path, body)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != body {
		t.Fatalf("a group/world-readable file was rewritten:\n%s", after)
	}
	if _, err := os.Stat(path + ".legacy-auth.bak"); !os.IsNotExist(err) {
		t.Fatalf("a backup was written for an untrusted file: %v", err)
	}
}

func TestLegacyAuthUnwritableFileIsHonouredForTheRun(t *testing.T) {
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
	if cfg.ConduytCrmBearerAuth != legacyTokenA || !strings.Contains(notices.String(), "used for this run only") {
		t.Fatalf("unwritable legacy config: honoured=%t notices=%q", cfg.ConduytCrmBearerAuth == legacyTokenA, notices.String())
	}
	assertNoSecret(t, "notice", notices.String())
}
