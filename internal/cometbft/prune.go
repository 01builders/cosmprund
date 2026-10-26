// Package cometbft prunes blocks and consensus state from CometBFT (or
// Tendermint) databases.
package cometbft

import (
	"errors"
	"fmt"
	"log/slog"

	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	dbm "github.com/cosmos/cosmos-db"

	cpdb "github.com/01builders/cosmprund/v2/internal/db"
)

// Result summarizes a pruning run.
type Result struct {
	Pruned  uint64
	OldBase int64
	NewBase int64
	Height  int64
}

// Prune keeps the last keep blocks (and their consensus state). It mirrors
// what a node does for min-retain-blocks: headers and commits that may still be
// needed as evidence are preserved by CometBFT itself.
func Prune(blockDB, stateDB dbm.DB, keep int64, logger *slog.Logger) (Result, error) {
	if keep < 1 {
		return Result{}, fmt.Errorf("blocks to keep must be at least 1, got %d", keep)
	}
	state, err := load(blockDB, stateDB)
	if err != nil {
		return Result{}, err
	}

	blockStore := store.NewBlockStore(cpdb.CometBFT{DB: blockDB})
	stateStore := sm.NewStore(cpdb.CometBFT{DB: stateDB}, sm.StoreOptions{})

	res := Result{OldBase: blockStore.Base(), Height: blockStore.Height()}
	res.NewBase = res.OldBase

	retain := res.Height - keep + 1
	if res.Height == 0 || retain <= res.OldBase {
		logger.Info("block store already within retention", "base", res.OldBase, "height", res.Height, "keep", keep)
		return res, nil
	}
	if state.LastBlockHeight < retain {
		return res, fmt.Errorf("consensus state is at height %d, below retain height %d", state.LastBlockHeight, retain)
	}

	logger.Info("pruning block store", "base", res.OldBase, "height", res.Height, "retain", retain)
	pruned, evidenceHeight, err := blockStore.PruneBlocks(retain, state)
	if err != nil {
		return res, fmt.Errorf("prune blocks: %w", err)
	}
	res.Pruned = pruned
	res.NewBase = blockStore.Base()

	logger.Info("pruning state store", "from", res.OldBase, "to", retain)
	if err := stateStore.PruneStates(res.OldBase, retain, evidenceHeight); err != nil {
		return res, fmt.Errorf("prune states: %w", err)
	}
	return res, nil
}

// Check validates the databases without modifying them, so callers can refuse
// up front instead of leaving a node partially pruned.
func Check(blockDB, stateDB dbm.DB) error {
	_, err := load(blockDB, stateDB)
	return err
}

func load(blockDB, stateDB dbm.DB) (sm.State, error) {
	if err := checkKeyLayout(blockDB, "blockstore"); err != nil {
		return sm.State{}, err
	}
	if err := checkKeyLayout(stateDB, "state"); err != nil {
		return sm.State{}, err
	}
	state, err := sm.NewStore(cpdb.CometBFT{DB: stateDB}, sm.StoreOptions{}).Load()
	if err != nil {
		return sm.State{}, fmt.Errorf("load consensus state: %w", err)
	}
	if state.IsEmpty() {
		return sm.State{}, errors.New("consensus state is empty; refusing to prune")
	}
	return state, nil
}

// checkKeyLayout rejects databases using CometBFT v1's ordered "v2" key
// layout, which the v0.38 store code linked here cannot read. Databases
// written by Tendermint v0.34 through CometBFT v1 with the default layout
// either have no marker or are marked "v1".
func checkKeyLayout(db dbm.DB, name string) error {
	v, err := db.Get([]byte("version"))
	if err != nil {
		return fmt.Errorf("%s: read key layout: %w", name, err)
	}
	if len(v) == 0 || string(v) == "v1" {
		return nil
	}
	return fmt.Errorf("%s: unsupported CometBFT key layout %q; use the node's built-in pruning", name, v)
}
