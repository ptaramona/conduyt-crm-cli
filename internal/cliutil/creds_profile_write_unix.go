// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.

//go:build !windows

package cliutil

// PATCH(novel1219-auth-compat): set the current bearer key in an explicit profile's existing credentials file.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

// SetProfileBearer sets crm_bearer_auth (value "" = explicitly cleared) in the explicit profile's credentials file,
// when that file exists, leaving every other key as it is. Every step is anchored to one descriptor of the profile's
// data directory, opened like the loader opens it (O_NOFOLLOW|O_DIRECTORY, verified owner-only): the existing file is
// read through openat and verified, the new bytes go to an O_EXCL owner-only temp file created in that directory and
// verified through its own descriptor before renameat publishes it. No pathname is resolved twice, nothing is deleted,
// and a missing file is not created (the loader then falls through to the global file, which the normal saver writes).
func SetProfileBearer(configPath, value string) error {
	path, err := CredentialsFilePathForConfig(configPath)
	if err != nil {
		return err
	}
	profileDir, dataName, fileName, err := explicitCredentialParts(path)
	if err != nil {
		return err
	}
	profile, err := os.Open(profileDir) // #nosec G304 -- explicit profile directory chosen by --config.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer profile.Close()
	dataDir, err := openatNoFollow(int(profile.Fd()), dataName, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		// A missing, symlinked or non-directory data entry holds nothing the loader would read (it refuses a
		// symlinked profile data directory), so there is nothing to set; it is never followed.
		var trust *credentialTrustError
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.As(err, &trust) {
			return nil
		}
		return fmt.Errorf("profile credentials directory %s: %w", filepath.Join(profileDir, dataName), err)
	}
	defer dataDir.Close()
	dataInfo, err := dataDir.Stat()
	if err != nil {
		return err
	}
	if err := verifyUnixCredentialDir(dataDir, dataInfo, filepath.Join(profileDir, dataName)); err != nil {
		return nil // an untrusted data directory: the loader refuses it, so it is left alone
	}
	dirfd := int(dataDir.Fd())
	current, err := openatNoFollow(dirfd, fileName, syscall.O_RDONLY)
	if err != nil {
		var trust *credentialTrustError
		if errors.Is(err, os.ErrNotExist) || errors.As(err, &trust) {
			return nil
		}
		return err
	}
	data, err := readVerifiedCredential(current, path)
	current.Close()
	if err != nil {
		var trust *credentialTrustError
		if errors.As(err, &trust) {
			return nil // an unsafe file: the loader refuses it (and suppresses the global fallback); left alone
		}
		return err
	}
	doc := map[string]any{}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	doc["crm_bearer_auth"] = value
	out, err := toml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	tmpName := fmt.Sprintf(".%s.%d.%d.tmp", fileName, os.Getpid(), time.Now().UnixNano())
	fd, err := unix.Openat(dirfd, tmpName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("creating temporary credentials file: %w", err)
	}
	tmp := os.NewFile(uintptr(fd), filepath.Join(profileDir, dataName, tmpName))
	discard := func() { _ = unix.Unlinkat(dirfd, tmpName, 0) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		discard()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		discard()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		discard()
		return err
	}
	// Verify the temp file through its own descriptor before it is published.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		discard()
		return err
	}
	if _, err := readVerifiedCredential(tmp, path); err != nil {
		tmp.Close()
		discard()
		return fmt.Errorf("temporary credentials file is not owner-only: %w", err)
	}
	if err := tmp.Close(); err != nil {
		discard()
		return err
	}
	if err := unix.Renameat(dirfd, tmpName, dirfd, fileName); err != nil {
		discard()
		return fmt.Errorf("publishing %s: %w", path, err)
	}
	return nil
}
