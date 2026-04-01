package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/schollz/progressbar/v3"
)

var cli struct {
	ArchivePrefix string   `help:"Archive prefix to filter against"`
	SubPath       string   `help:"Path inside each archive to cd into before staring backup"`
	Hostname      string   `help:"Hostname to set for all matching archives. Keep unset to use real hostname"`
	ResticOpts    []string `arg:"" optional:"" passthrough:"partial"`
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

	if err := run(); err != nil {
		slog.Error(err.Error())
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// open borg repo
	br := &BorgRepo{}

	slog.Info("Listing borg archives")
	if err := br.LoadBorgArchives(ctx); err != nil {
		return fmt.Errorf("listing borg archives: %w", err)
	}

	slog.Info("Found archives", "count", len(br.Archives))

	// prepare temporary folder to mount repo into
	mountDir, err := os.MkdirTemp("", "borg2restic")
	if err != nil {
		return fmt.Errorf("creating temporary folder: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(mountDir)
	}()

	// mount repo to a temporary folder
	slog.Info("Mounting borg repo", "path", mountDir)
	if err := br.Mount(ctx, mountDir); err != nil {
		return fmt.Errorf("mounting repo to %v: %w", mountDir, err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = br.Unmount(ctx)
	}()

	if cli.Hostname != "" {
		const hostEnv = "RESTIC_HOST"
		if err := os.Setenv(hostEnv, cli.Hostname); err != nil {
			return fmt.Errorf("setting "+hostEnv+": %w", err)
		}
	}

	if len(cli.ResticOpts) != 0 && cli.ResticOpts[0] == "--" {
		cli.ResticOpts = cli.ResticOpts[1:]
	}

	if !slices.Contains(cli.ResticOpts, "--stdin-from-command") && !slices.Contains(cli.ResticOpts, "--stdin") {
		// we backup ".", and set cmd.Dir appropriately
		cli.ResticOpts = append(cli.ResticOpts, ".")
	}

	bar := progressbar.Default(int64(len(br.Archives)))
	errs := make([]error, 0, len(br.Archives))

	for archive := range br.FilterArchives(cli.ArchivePrefix) {
		_ = bar.Clear()
		slog.Info("Migrating archive", "name", archive.Archive)
		_ = bar.Add(1)

		archiveDir := filepath.Join(mountDir, archive.Name)

		// assemble restic backup command
		// Example: restic backup --force -H tp --time "2016-11-04 00:00:00" --set-path / .
		args := append([]string{
			"backup",
			"--force",
			"--time=" + archive.GetStartTime().Format("2006-01-02 15:04:05"),
		}, cli.ResticOpts...)

		// prepare command
		cmd := execCmd(ctx, "restic", args...)
		cmd.Dir = archiveDir
		if cli.SubPath != "" {
			cmd.Dir = filepath.Join(archiveDir, cli.SubPath)
		}

		if err := cmd.Run(); err != nil {
			slog.Error("Restic backup failed", "error", err)
			errs = append(errs, fmt.Errorf("archive %q: %w", archive.Archive, err))
			if errors.Is(err, context.Canceled) || strings.HasPrefix(err.Error(), "signal: ") {
				break
			}
		}
	}

	unmountCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	slog.Info("Unmounting repo")

	if err := br.Unmount(unmountCtx); err != nil {
		return fmt.Errorf("unmounting repo: %w", err)
	}

	if len(errs) != 0 {
		errs = append([]error{errors.New("archives failed")}, errs...)
		return errors.Join(errs...)
	}
	return nil
}
