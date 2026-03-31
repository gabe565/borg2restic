package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/alecthomas/kong"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
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

	slog.SetDefault(slog.New(
		tint.NewHandler(os.Stderr, &tint.Options{
			Level:      slog.LevelDebug,
			TimeFormat: time.TimeOnly,
			NoColor:    !(isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())),
		}),
	))

	err := run()
	if err != nil {
		slog.Error(err.Error())
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// open borg repo
	br := &BorgRepo{}

	slog.Info("Listing borg archives")
	err := br.LoadBorgArchives(ctx)
	if err != nil {
		return fmt.Errorf("error loading borg archives: %w", err)
	}

	slog.Info("Found archives", "count", len(br.Archives))

	// prepare temporary folder to mount repo into
	mountDir, err := os.MkdirTemp("", "borg2restic")
	if err != nil {
		return fmt.Errorf("unable to create temporary folder: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(mountDir)
	}()

	// mount repo to a temporary folder
	slog.Info("Mounting borg repo", "path", mountDir)
	err = br.Mount(ctx, mountDir)
	if err != nil {
		return fmt.Errorf("unable to mount repo to %v: %w", mountDir, err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = br.Unmount(ctx)
	}()

	// initialize progressbar
	bar := progressbar.Default(int64(len(br.Archives)))

	for archive := range br.FilterArchives(cli.ArchivePrefix) {
		_ = bar.Clear()
		slog.Info("Migrating archive", "name", archive.Archive)
		_ = bar.Add(1)

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
			return fmt.Errorf("error running restic: %v", err)
		}
	}

	unmountCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	slog.Info("Unmounting repo")

	err = br.Unmount(unmountCtx)
	if err != nil {
		return fmt.Errorf("unable to unmount repo: %w", err)
	}

	return nil
}
