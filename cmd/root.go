package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

const appName = "cosmprund"

// Version and Commit are set at build time via -ldflags.
var (
	Version = "dev"
	Commit  = ""
)

// NewRootCmd returns the root command.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          appName,
		Short:        "cosmprund prunes block and application history from a stopped Cosmos SDK / CometBFT node",
		Version:      Version,
		SilenceUsage: true,
	}
	if Commit != "" {
		rootCmd.Version = Version + " (" + Commit + ")"
	}
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.AddCommand(pruneCmd())
	return rootCmd
}

// Execute runs the root command. It is called by main.main().
func Execute() {
	cobra.EnableCommandSorting = false

	// Pruning calls cannot be interrupted midway; the context only stops work
	// that has not started yet. A second signal kills the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		stop()
		os.Exit(1)
	}
}
