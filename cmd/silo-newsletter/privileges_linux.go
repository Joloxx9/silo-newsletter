//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// dropRoot gives the data directory to PUID:PGID (Unraid's nobody:users by
// default) and switches to that user, so a root-owned appdata folder works
// without a manual chown. It does nothing when not started as root.
func dropRoot(directory string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	uid, err := envID("PUID", 99)
	if err != nil {
		return err
	}
	gid, err := envID("PGID", 100)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	err = filepath.WalkDir(directory, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
	if err != nil {
		return fmt.Errorf("nie udało się ustawić właściciela %s: %w", directory, err)
	}
	if err := syscall.Setgroups(nil); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	return syscall.Setuid(uid)
}

func envID(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("%s musi być liczbą, jest %q", name, value)
	}
	return id, nil
}
