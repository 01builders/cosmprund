package cometbft_test

import (
	"io"
	"log/slog"
	"testing"

	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/stretchr/testify/require"

	"github.com/01builders/cosmprund/v2/internal/cometbft"
	cpdb "github.com/01builders/cosmprund/v2/internal/db"
	"github.com/01builders/cosmprund/v2/internal/testutil"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPrune(t *testing.T) {
	for _, backend := range []cpdb.Backend{cpdb.BackendGoLevelDB, cpdb.BackendPebbleDB} {
		for _, expired := range []bool{true, false} {
			name := string(backend)
			if expired {
				name += "/evidence-expired"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				blockDB := testutil.NewDB(t, dir, "blockstore", backend)
				defer testutil.CloseDB(t, blockDB)
				stateDB := testutil.NewDB(t, dir, "state", backend)
				defer testutil.CloseDB(t, stateDB)
				testutil.WriteChain(t, blockDB, stateDB, 30, expired)

				res, err := cometbft.Prune(blockDB, stateDB, 10, discard())
				require.NoError(t, err)
				require.EqualValues(t, 20, res.Pruned)
				require.EqualValues(t, 1, res.OldBase)
				require.EqualValues(t, 21, res.NewBase)

				bs := store.NewBlockStore(cpdb.CometBFT{DB: blockDB})
				require.EqualValues(t, 21, bs.Base())
				require.EqualValues(t, 30, bs.Height())
				require.Nil(t, bs.LoadBlock(20))
				require.NotNil(t, bs.LoadBlock(21))
				// CometBFT keeps headers inside the evidence window.
				require.Equal(t, !expired, bs.LoadBlockMeta(20) != nil)

				ss := sm.NewStore(cpdb.CometBFT{DB: stateDB}, sm.StoreOptions{})
				_, err = ss.LoadValidators(21)
				require.NoError(t, err)
				if expired {
					_, err = ss.LoadValidators(15)
					require.Error(t, err)
				}
				state, err := ss.Load()
				require.NoError(t, err)
				require.EqualValues(t, 30, state.LastBlockHeight)

				// A second run is a no-op.
				res, err = cometbft.Prune(blockDB, stateDB, 10, discard())
				require.NoError(t, err)
				require.Zero(t, res.Pruned)
			})
		}
	}
}

func TestPruneKeepMoreThanHeight(t *testing.T) {
	dir := t.TempDir()
	blockDB := testutil.NewDB(t, dir, "blockstore", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, blockDB)
	stateDB := testutil.NewDB(t, dir, "state", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, stateDB)
	testutil.WriteChain(t, blockDB, stateDB, 5, true)

	res, err := cometbft.Prune(blockDB, stateDB, 100, discard())
	require.NoError(t, err)
	require.Zero(t, res.Pruned)
	require.NotNil(t, store.NewBlockStore(cpdb.CometBFT{DB: blockDB}).LoadBlock(1))
}

func TestPruneRejectsV2KeyLayout(t *testing.T) {
	dir := t.TempDir()
	blockDB := testutil.NewDB(t, dir, "blockstore", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, blockDB)
	stateDB := testutil.NewDB(t, dir, "state", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, stateDB)
	require.NoError(t, blockDB.Set([]byte("version"), []byte("v2")))

	_, err := cometbft.Prune(blockDB, stateDB, 1, discard())
	require.ErrorContains(t, err, `unsupported CometBFT key layout "v2"`)
}

func TestPruneValidatesKeep(t *testing.T) {
	_, err := cometbft.Prune(nil, nil, 0, discard())
	require.ErrorContains(t, err, "at least 1")
}
