// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

//go:build windows

package cliutil

// PATCH(novel1219-auth-compat): on Windows the CLI has no writer that sets a private DACL before publishing, so an
// explicit profile's credentials file is never rewritten; an existing one is reported instead.

import (
	"errors"
	"fmt"
	"os"
)

// SetProfileBearer refuses to rewrite an explicit profile's credentials file on Windows. A missing file needs no
// write (the loader falls through to the global file). An existing one makes the caller fail with this error, which
// names the file to edit or remove by hand, rather than leave a stale credential authoritative.
func SetProfileBearer(configPath, value string) error {
	path, err := CredentialsFilePathForConfig(configPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("the profile credentials file %s cannot be rewritten safely on Windows; set crm_bearer_auth in it by hand (or remove it), or use CONDUYT_CRM_BEARER_AUTH", path)
}
