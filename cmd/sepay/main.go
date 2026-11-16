// Command sepay is the SePay CLI entry point.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/update"
	"github.com/sepayvn/sepay-cli/pkg/cmd/root"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

func main() {
	// os.Exit skips defers, so run() holds all deferred work.
	os.Exit(int(run()))
}

func run() cmdutil.ExitCode {
	f := cmdutil.New()
	rootCmd := root.NewCmdRoot(f)

	// Every command reads this context via cmd.Context().
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	exe, err := os.Executable()
	if err == nil {
		// Homebrew and npm start sepay through a symlink; the channel shows in the target.
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		// The binary a Windows "sepay update" moved aside could not be deleted while it ran.
		update.RemoveOld(exe)
	}

	newer := checkForUpdate(ctx)

	cmd, err := rootCmd.ExecuteContextC(ctx)
	if err != nil {
		// A cancelled context surfaces as context.Canceled or a transport error naming the signal.
		if ctx.Err() != nil {
			err = cmdutil.ErrInterrupted
		}
		f.UsageCommand = cmd.CommandPath()
		return cmdutil.HandleError(f, err)
	}
	// "sepay update" has just answered the question the notice asks.
	if f.Global.Output == "table" && !f.Global.Quiet && cmd.CommandPath() != "sepay update" {
		select {
		case res := <-newer:
			fmt.Fprint(f.IOStreams.ErrOut, update.Notice(build.Version, res, exe))
		case <-time.After(time.Second):
		}
	}
	return cmdutil.ExitOK
}

// checkForUpdate runs the daily release check alongside the command.
func checkForUpdate(ctx context.Context) <-chan update.Result {
	ch := make(chan update.Result, 1)
	if !update.Enabled() {
		ch <- update.Result{}
		return ch
	}
	go func() {
		var res update.Result
		if dir, err := config.Dir(); err == nil {
			res, _ = update.New(dir).Check(ctx, build.Version)
		}
		ch <- res
	}()
	return ch
}
