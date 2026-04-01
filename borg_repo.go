package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"strconv"
	"strings"
	"time"
)

type BorgRepo struct {
	Archives []*BorgArchive `json:"archives"`

	mountPoint string
}

func (br *BorgRepo) LoadBorgArchives(ctx context.Context) error {
	// obtain a listing of the repo
	cmd := execCmd(ctx, "borg", "list", "--json")

	cmd.Stdout = nil
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("setting up pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("running borg list: %w", err)
	}

	jsonErr := json.NewDecoder(out).Decode(&br)

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("borg list failed: %w", err)
	}

	if jsonErr != nil {
		return fmt.Errorf("parsing borg list output: %w", jsonErr)
	}

	for _, borgArchive := range br.Archives {
		if err := borgArchive.ParseTimestamps(); err != nil {
			return fmt.Errorf("parsing timestamps for archive %v: %w", borgArchive.ID, err)
		}
	}

	return nil
}

// Mount mounts an repo at the chosen destination path
// archiveName can be left to the empty string, in that case,
// a listing of all archives is provided at the root of the mount
func (br *BorgRepo) Mount(ctx context.Context, dest string) error {
	if br.mountPoint != "" {
		return fmt.Errorf("already mounted at %v", br.mountPoint)
	}

	uid := os.Getuid()
	gid := os.Getgid()
	permOpt := "uid=" + strconv.Itoa(uid) + ",gid=" + strconv.Itoa(gid)

	args := []string{"mount", "-o", permOpt, "::", dest}
	cmd := execCmd(ctx, "borg", args...)
	br.mountPoint = dest

	if err := cmd.Run(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for {
		entries, err := os.ReadDir(dest)
		if err == nil && len(entries) != 0 {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for borg mount to become ready: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}

	return nil
}

// Unmount does unmount the repo.
func (br *BorgRepo) Unmount(ctx context.Context) error {
	if br.mountPoint == "" {
		return fmt.Errorf("nothing mounted")
	}

	if _, err := os.Stat(br.mountPoint); errors.Is(err, os.ErrNotExist) {
		return err
	}

	cmd := execCmd(ctx, "borg", "umount", br.mountPoint)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unmounting repo: %w", err)
	}

	if err := os.Remove(br.mountPoint); err != nil {
		return fmt.Errorf("removing mount point: %w", err)
	}

	br.mountPoint = ""

	return nil
}

func (br *BorgRepo) FilterArchives(prefix string) iter.Seq[*BorgArchive] {
	return func(yield func(*BorgArchive) bool) {
		for _, archive := range br.Archives {
			if archive == nil || !strings.HasPrefix(archive.Name, prefix) {
				continue
			}

			if !yield(archive) {
				return
			}
		}
	}
}
