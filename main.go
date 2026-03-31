package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/schollz/progressbar/v3"
)

var cli struct {
	ArchivePrefix string `help:"Archive prefix to filter against"`
	SubPath       string `help:"Path inside each archive to cd into before staring backup"`
	Hostname      string `help:"Hostname to set for all matching archives. Keep unset to use real hostname"`
	SetPath       string `help:"Optionally override path (via restic --set-path)"`
}

func main() {
	_ = kong.Parse(&cli,
		kong.Name("borg2restic"),
		kong.Description("A tool to help convert a borg repository to restic"),
		kong.UsageOnError(),
	)

	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// open borg repo
	br := &BorgRepo{}
	err := br.LoadBorgArchives(ctx)
	if err != nil {
		return fmt.Errorf("error loading borg archives: %w", err)
	}

	// prepare temporary folder to mount repo into
	mountDir, err := os.MkdirTemp("", "borg2restic")
	if err != nil {
		return fmt.Errorf("unable to create temporary folder: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(mountDir)
	}()

	// mount repo to a temporary folder
	err = br.Mount(ctx, mountDir)
	if err != nil {
		return fmt.Errorf("unable to mount repo to %v: %w", mountDir, err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = br.Unmount(ctx)
	}()

	fmt.Printf("mounted at %v\n", mountDir)

	// filter out archives not matching the prefix
	filteredArchives := []*BorgArchive{}

	// loop over all archives
	for _, archive := range br.Archives {
		// if the archive name matches the prefix, add to list
		if strings.HasPrefix(archive.Name, cli.ArchivePrefix) {
			filteredArchives = append(filteredArchives, archive)
		}
	}

	// initialize progressbar
	bar := progressbar.Default(int64(len(filteredArchives)))

	for i, archive := range filteredArchives {

		err := bar.Set(i)
		if err != nil {
			return fmt.Errorf("error setting bar to %v: %w", i, err)
		}

		archiveDir := filepath.Join(mountDir, archive.Name)

		// assemble restic backup command
		// Example: restic backup --force -H tp --time "2016-11-04 00:00:00" --set-path / .
		args := []string{"backup", "--force"}

		// set hostname if set
		if cli.Hostname != "" {
			args = append(args, "-H", cli.Hostname)
		}

		// set time from repo time
		args = append(args,
			"--time",
			// restic wants this date format:
			// 2006-01-02 15:04:05
			archive.GetStartTime().Format("2006-01-02 15:04:05"),
		)

		// set path if setPath is set
		if cli.SetPath != "" {
			args = append(args,
				"--set-path",
				cli.SetPath,
			)
		}

		// we backup ".", and set cmd.Dir appropriately
		args = append(args, ".")

		// prepare command
		cmd := execCmd(ctx, "restic", args...)
		cmd.Dir = archiveDir
		if cli.SubPath != "" {
			cmd.Dir = filepath.Join(archiveDir, cli.SubPath)
		}

		bar.Describe(fmt.Sprintf("Importing Archive %v (%v+)", archive.Archive, args))

		err = cmd.Run()
		if err != nil {
			return err
		}
	}

	unmountCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = br.Unmount(unmountCtx)
	if err != nil {
		return err
	}

	return nil
}
