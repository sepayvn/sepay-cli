// Package update implements "sepay update".
package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/internal/envvar"
	upd "github.com/sepayvn/sepay-cli/internal/update"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type options struct {
	version string
	check   bool

	executable func() (string, error)
	updater    func() *upd.Updater
}

// status is the --check result.
type status struct {
	Current         string      `json:"current"`
	Latest          string      `json:"latest"`
	UpdateAvailable bool        `json:"update_available"`
	Channel         upd.Channel `json:"channel"`
	UpdateCommand   string      `json:"update_command"`
}

// result is what an update did; Updated is false when sepay was already at To.
type result struct {
	Updated bool   `json:"updated"`
	From    string `json:"from"`
	To      string `json:"to"`
	Path    string `json:"path"`
}

// NewCmdUpdate builds the "sepay update" command.
func NewCmdUpdate(f *cmdutil.Factory) *cobra.Command {
	return newCmdUpdate(f, &options{executable: executable, updater: upd.NewUpdater})
}

func newCmdUpdate(f *cmdutil.Factory, opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update sepay to the latest release",
		Long: `Replace this sepay binary with a release from GitHub Releases.

The archive must match its SHA-256 in the release's checksums.txt. When cosign is on PATH,
checksums.txt must also carry the signature of the sepay-cli release workflow.
SEPAY_REQUIRE_SIGNATURE=1 refuses to update without cosign.

Only a binary installed by the install script or unpacked from a release archive is replaced.
One installed with npm, Homebrew, Scoop or go install is updated by that tool; sepay update
prints its command and exits 1. --check works for every install.`,
		Example: `  sepay update
  sepay update --check --json
  sepay update --version 0.2.0 --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.check && opts.version != "" {
				return &cmdutil.FlagError{Err: errors.New("--check and --version are mutually exclusive")}
			}
			return runUpdate(cmd.Context(), f, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&opts.version, "version", "", "Install this release instead of the latest, e.g. 0.2.0")
	fl.BoolVar(&opts.check, "check", false, "Report whether a newer release exists without installing it")
	return cmd
}

// executable is the running binary with its symlinks resolved: the install channel shows in the target.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func runUpdate(ctx context.Context, f *cmdutil.Factory, opts *options) error {
	target := strings.TrimPrefix(opts.version, "v")
	if opts.version != "" && !upd.ValidVersion(target) {
		return &cmdutil.FlagError{Err: fmt.Errorf("invalid value %q for --version (must look like 0.2.0 or 0.2.0-rc.1)", opts.version)}
	}
	// A dev, snapshot or pseudo-version build has no release to compare with.
	if build.ReleaseURL() == "" {
		return errors.New("this build has no release to update to")
	}
	current := build.Number()
	exe, err := opts.executable()
	if err != nil {
		return fmt.Errorf("cannot find the running sepay binary: %w", err)
	}
	channel := upd.DetectChannel(exe)
	u := opts.updater()

	if opts.check {
		latest, err := u.Latest(ctx)
		if err != nil {
			return fmt.Errorf("could not find the latest release: %w", err)
		}
		return renderStatus(f, status{
			Current:         current,
			Latest:          latest,
			UpdateAvailable: upd.Newer(latest, current),
			Channel:         channel,
			UpdateCommand:   channel.UpdateCommand(current, latest),
		})
	}
	if channel != upd.ChannelStandalone {
		if target == "" {
			return fmt.Errorf("sepay was installed with %s; update it with: %s", channel.Title(), channel.UpdateCommand(current, ""))
		}
		if cmd := channel.InstallCommand(target); cmd != "" {
			return fmt.Errorf("sepay was installed with %s; install %s with: %s", channel.Title(), target, cmd)
		}
		return fmt.Errorf("sepay was installed with %s, which installs only its newest release, not %s", channel.Title(), target)
	}

	if target == "" {
		latest, err := u.Latest(ctx)
		if err != nil {
			return fmt.Errorf("could not find the latest release: %w", err)
		}
		// A prerelease newer than the latest release stays: update never downgrades unasked.
		if upd.Newer(latest, current) {
			target = latest
		}
	}
	if target == "" || target == current {
		return f.RenderOutput(result{From: current, To: current, Path: exe}, func() error {
			f.Printf("%s sepay %s is up to date\n", f.SuccessIcon(), current)
			return nil
		})
	}

	if u.Cosign == nil && envvar.Enabled(upd.RequireSignatureEnv) {
		return fmt.Errorf("%s is set but cosign is not on PATH; install cosign (https://docs.sigstore.dev) or unset it", upd.RequireSignatureEnv)
	}

	if !f.Global.Yes {
		if !f.CanPrompt() {
			return &cmdutil.FlagError{Err: errors.New("pass --yes to update without a prompt")}
		}
		ok, err := f.AskYesNo(fmt.Sprintf("Update sepay %s → %s at %s?", current, target, exe), false)
		if err != nil {
			return err
		}
		if !ok {
			return &cmdutil.FlagError{Err: errors.New("update cancelled")}
		}
	}

	progress := f.Global.Output == "table" && !f.Global.Quiet
	if progress {
		f.ErrPrintf("Downloading %s...\n", u.Archive(target))
	}
	if u.Cosign == nil {
		f.ErrPrintf("! cosign not found, checking SHA-256 only\n")
	}
	dir := filepath.Dir(exe)
	staged, err := u.Fetch(ctx, target, dir)
	if err != nil {
		return writeError(err, dir, fmt.Errorf("could not update to %s: %w", target, err))
	}
	// Replace renames staged away; on any earlier exit it is removed here.
	defer func() { _ = os.Remove(staged) }()
	if progress {
		verified := "SHA-256 verified"
		if u.Cosign != nil {
			verified = "Signature and SHA-256 verified"
		}
		f.ErrPrintf("%s %s\n", f.SuccessIcon(), verified)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := upd.Replace(exe, staged); err != nil {
		return writeError(err, dir, fmt.Errorf("could not replace %s: %w", exe, err))
	}

	return f.RenderOutput(result{Updated: true, From: current, To: target, Path: exe}, func() error {
		f.Printf("%s Updated sepay %s → %s\n", f.SuccessIcon(), current, target)
		f.Printf("Release notes: %s\n", upd.ReleaseNotesURL(target))
		return nil
	})
}

func renderStatus(f *cmdutil.Factory, s status) error {
	return f.RenderOutput(s, func() error {
		if !s.UpdateAvailable {
			f.Printf("%s sepay %s is up to date\n", f.SuccessIcon(), s.Current)
			return nil
		}
		f.Printf("A new release of sepay is available: %s → %s\n", s.Current, s.Latest)
		f.Printf("To update: %s\n", s.UpdateCommand)
		return nil
	})
}

// writeError names the fix when dir is not writable, else returns fallback.
func writeError(err error, dir string, fallback error) error {
	if !errors.Is(err, fs.ErrPermission) {
		return fallback
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("cannot write to %s; run sepay update from a terminal opened as administrator", dir)
	}
	return fmt.Errorf("cannot write to %s; run: sudo sepay update", dir)
}
