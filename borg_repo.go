package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

type BorgRepo struct {
	Archives []*BorgArchive `json:"archives"`

	mountPoint string
}

func (br *BorgRepo) LoadBorgArchives(ctx context.Context) error {
	// obtain a listing of the repo
	cmd := execCmd(ctx, "borg", "list", "--json")
	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("unable to run borg list: %w", err)
	}

	err = json.Unmarshal(out.Bytes(), &br)
	if err != nil {
		return fmt.Errorf("unable to serialize borg list output: %w", err)
	}

	for _, borgArchive := range br.Archives {
		err := borgArchive.ParseTimestamps()
		if err != nil {
			return fmt.Errorf("unable to parse timestamps for archive %v: %w", borgArchive.ID, err)
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

	args := []string{"mount", "-o", "ignore_permissions", "::", dest}
	cmd := execCmd(ctx, "borg", args...)
	fmt.Printf("%+v", args)

	br.mountPoint = dest

	return cmd.Run()
}

// Unmount does unmount the repo.
func (br *BorgRepo) Unmount(ctx context.Context) error {
	if br.mountPoint == "" {
		return fmt.Errorf("nothing mounted")
	}

	cmd := execCmd(ctx, "fusermount", "-u", br.mountPoint)
	return cmd.Run()
}
