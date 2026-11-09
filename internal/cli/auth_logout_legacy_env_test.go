// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

// PATCH(novel1219-auth-compat): Codex novel1219-r8 P2: logout reports a legacy env name that still supplies the token,
// exactly as it reports CONDUYT_CRM_BEARER_AUTH, in JSON and in prose.

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ptaramona/conduyt-crm-cli/internal/cliutil/testenv"
)

func TestAuthLogoutReportsLegacyEnvNames(t *testing.T) {
	for _, name := range []string{"CONDUYT_BEARER_AUTH", "CONDUYT_API_KEY"} {
		t.Run(name, func(t *testing.T) {
			for _, asJSON := range []bool{true, false} {
				testenv.Isolate(t)
				t.Setenv("CONDUYT_CRM_BEARER_AUTH", "")
				_ = os.Unsetenv("CONDUYT_CRM_BEARER_AUTH")
				t.Setenv("CONDUYT_BEARER_AUTH", "")
				_ = os.Unsetenv("CONDUYT_BEARER_AUTH")
				t.Setenv("CONDUYT_API_KEY", "")
				_ = os.Unsetenv("CONDUYT_API_KEY")
				t.Setenv(name, "LEGACY-ENV-SECRET-6666")
				args := []string{"auth", "logout"}
				if asJSON {
					args = append(args, "--json")
				}
				// Not runNovel: it exports CONDUYT_CRM_BEARER_AUTH, which would decide before the legacy names.
				cmd := RootCmd()
				cmd.SetArgs(args)
				var outBuf, errBuf bytes.Buffer
				cmd.SetOut(&outBuf)
				cmd.SetErr(&errBuf)
				err := cmd.Execute()
				out, stderr := outBuf.String(), errBuf.String()
				if err != nil {
					t.Fatalf("%v: %v", args, err)
				}
				if !strings.Contains(out, name+" env var is still set") || strings.Contains(out, "Logged out. Credentials cleared.") {
					t.Fatalf("%v: output %q does not name %s as still set", args, out, name)
				}
				if strings.Contains(out+stderr, "LEGACY-ENV-SECRET-6666") {
					t.Fatalf("%v leaks the env value", args)
				}
			}
		})
	}
}
