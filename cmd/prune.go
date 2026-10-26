package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/01builders/cosmprund/v2/internal/app"
	"github.com/01builders/cosmprund/v2/internal/cometbft"
	cpdb "github.com/01builders/cosmprund/v2/internal/db"
)

// maxKeep keeps flag values representable as positive int64 heights.
const maxKeep = 1 << 62

type pruneFlags struct {
	blocks    uint64
	versions  uint64
	backend   string
	cosmosSDK bool
	cometBFT  bool
	compact   bool
	workers   int
}

func pruneCmd() *cobra.Command {
	var f pruneFlags
	cmd := &cobra.Command{
		Use:   "prune [path_to_data_dir]",
		Short: "prune blocks, consensus state and application state; the node must be stopped",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPrune(cmd.Context(), args[0], f)
		},
	}

	fs := cmd.Flags()
	fs.Uint64VarP(&f.blocks, "blocks", "b", 10, "number of recent blocks to keep")
	fs.Uint64VarP(&f.versions, "versions", "v", 10, "number of recent application state versions to keep")
	fs.StringVar(&f.backend, "backend", string(cpdb.BackendAuto), "database backend: auto, goleveldb, pebbledb or rocksdb")
	fs.BoolVar(&f.cosmosSDK, "cosmos-sdk", true, "prune application state (set false for non Cosmos SDK chains)")
	fs.BoolVar(&f.cometBFT, "tendermint", true, "prune CometBFT/Tendermint blocks and state")
	fs.BoolVar(&f.compact, "compact", true, "compact databases after pruning to reclaim disk space")
	fs.IntVar(&f.workers, "workers", 4, "number of application stores pruned concurrently")
	return cmd
}

func runPrune(ctx context.Context, dataDir string, f pruneFlags) error {
	backend, err := cpdb.ParseBackend(f.backend)
	if err != nil {
		return err
	}
	if !f.cosmosSDK && !f.cometBFT {
		return errors.New("nothing to do: both --cosmos-sdk and --tendermint are false")
	}
	if f.blocks < 1 || f.blocks > maxKeep || f.versions < 1 || f.versions > maxKeep {
		return fmt.Errorf("--blocks and --versions must be between 1 and %d", uint64(maxKeep))
	}
	if f.workers < 1 {
		return errors.New("--workers must be at least 1")
	}
	if fi, err := os.Stat(dataDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("data directory %s not found", dataDir)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := pruneNode(ctx, dataDir, backend, f, logger); err != nil {
		logger.Error("pruning failed", "err", err)
		return err
	}
	logger.Info("pruning complete")
	return nil
}

// node holds the open databases of a data directory. A nil field means that
// part of the node is not being pruned.
type node struct {
	app, blocks, state dbm.DB
}

func pruneNode(ctx context.Context, dataDir string, backend cpdb.Backend, f pruneFlags, logger *slog.Logger) (err error) {
	var n node
	defer func() {
		for _, db := range []dbm.DB{n.app, n.blocks, n.state} {
			if db != nil {
				closeDB(db, &err)
			}
		}
	}()
	if err := n.open(dataDir, backend, f, logger); err != nil {
		return err
	}

	appOpts := app.Options{KeepVersions: int64(f.versions), Workers: f.workers, Logger: logger.With("db", "application")}
	cmtLogger := logger.With("db", "cometbft")

	// Validate everything before deleting anything, so an unsupported store
	// does not leave the node half pruned.
	if n.blocks != nil {
		if err := cometbft.Check(n.blocks, n.state); err != nil {
			return err
		}
	}
	if n.app != nil {
		if _, err := app.Check(ctx, n.app, appOpts); err != nil {
			return err
		}
	}

	// The databases are independent, so prune them concurrently.
	g, gctx := errgroup.WithContext(ctx)
	if n.blocks != nil {
		g.Go(func() error { return pruneCometBFT(n.blocks, n.state, f, cmtLogger) })
	}
	if n.app != nil {
		g.Go(func() error { return pruneApp(gctx, n.app, appOpts, f.compact) })
	}
	return g.Wait()
}

func (n *node) open(dataDir string, backend cpdb.Backend, f pruneFlags, logger *slog.Logger) error {
	targets := []struct {
		enabled bool
		name    string
		dst     *dbm.DB
	}{
		{f.cosmosSDK, "application", &n.app},
		{f.cometBFT, "blockstore", &n.blocks},
		{f.cometBFT, "state", &n.state},
	}
	for _, t := range targets {
		if !t.enabled {
			continue
		}
		db, b, err := cpdb.Open(dataDir, t.name, backend)
		if err != nil {
			return err
		}
		*t.dst = db
		logger.Info("opened database", "db", t.name, "backend", b)
	}
	return nil
}

func pruneCometBFT(blockDB, stateDB dbm.DB, f pruneFlags, logger *slog.Logger) error {
	res, err := cometbft.Prune(blockDB, stateDB, int64(f.blocks), logger)
	if err != nil {
		return err
	}
	logger.Info("pruned blocks", "pruned", res.Pruned, "base", res.NewBase, "height", res.Height)

	if !f.compact || res.Pruned == 0 {
		return nil
	}
	logger.Info("compacting block store")
	if err := cpdb.Compact(blockDB); err != nil {
		return fmt.Errorf("compact block store: %w", err)
	}
	logger.Info("compacting state store")
	if err := cpdb.Compact(stateDB); err != nil {
		return fmt.Errorf("compact state store: %w", err)
	}
	return nil
}

func pruneApp(ctx context.Context, appDB dbm.DB, opts app.Options, compact bool) error {
	results, err := app.Prune(ctx, appDB, opts)
	if err != nil {
		return err
	}

	pruned := 0
	for _, r := range results {
		if r.Skipped != "" {
			opts.Logger.Info("skipped store", "store", r.Name, "reason", r.Skipped)
			continue
		}
		pruned++
	}
	opts.Logger.Info("pruned application stores", "pruned", pruned, "total", len(results))

	if !compact || pruned == 0 {
		return nil
	}
	opts.Logger.Info("compacting application state")
	if err := cpdb.Compact(appDB); err != nil {
		return fmt.Errorf("compact application state: %w", err)
	}
	return nil
}

func closeDB(db dbm.DB, err *error) {
	if cerr := db.Close(); cerr != nil {
		*err = errors.Join(*err, fmt.Errorf("close database: %w", cerr))
	}
}
