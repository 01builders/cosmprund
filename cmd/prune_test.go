package cmd

import (
	"context"
	"testing"

	"github.com/cometbft/cometbft/store"
	"github.com/stretchr/testify/require"

	"github.com/01builders/cosmprund/v2/internal/app"
	cpdb "github.com/01builders/cosmprund/v2/internal/db"
	"github.com/01builders/cosmprund/v2/internal/testutil"
)

// writeNode creates a data directory. Each database may use a different
// backend, as happens on nodes that migrated only some of them.
func writeNode(t *testing.T, appBackend, cometBackend cpdb.Backend) string {
	t.Helper()
	dir := t.TempDir()

	appDB := testutil.NewDB(t, dir, "application", appBackend)
	testutil.WriteAppStore(t, appDB, "bank", 30)
	testutil.WriteCommitInfo(t, appDB, 30, "bank")
	require.NoError(t, appDB.Close())

	blockDB := testutil.NewDB(t, dir, "blockstore", cometBackend)
	stateDB := testutil.NewDB(t, dir, "state", cometBackend)
	testutil.WriteChain(t, blockDB, stateDB, 30, true)
	require.NoError(t, blockDB.Close())
	require.NoError(t, stateDB.Close())
	return dir
}

func defaultFlags() pruneFlags {
	return pruneFlags{
		blocks: 5, versions: 5, backend: "auto",
		cosmosSDK: true, cometBFT: true, compact: true, workers: 2,
	}
}

func TestRunPrune(t *testing.T) {
	for _, tc := range []struct{ app, comet cpdb.Backend }{
		{cpdb.BackendGoLevelDB, cpdb.BackendGoLevelDB},
		{cpdb.BackendPebbleDB, cpdb.BackendPebbleDB},
		{cpdb.BackendPebbleDB, cpdb.BackendGoLevelDB},
	} {
		t.Run(string(tc.app)+"+"+string(tc.comet), func(t *testing.T) {
			dir := writeNode(t, tc.app, tc.comet)
			require.NoError(t, runPrune(context.Background(), dir, defaultFlags()))

			blockDB, _, err := cpdb.Open(dir, "blockstore", cpdb.BackendAuto)
			require.NoError(t, err)
			defer testutil.CloseDB(t, blockDB)
			bs := store.NewBlockStore(cpdb.CometBFT{DB: blockDB})
			require.EqualValues(t, 26, bs.Base())
			require.EqualValues(t, 30, bs.Height())
		})
	}
}

func TestRunPruneFlagValidation(t *testing.T) {
	dir := t.TempDir()
	for name, mutate := range map[string]func(*pruneFlags){
		"zero blocks":   func(f *pruneFlags) { f.blocks = 0 },
		"zero versions": func(f *pruneFlags) { f.versions = 0 },
		"huge blocks":   func(f *pruneFlags) { f.blocks = 1 << 63 },
		"zero workers":  func(f *pruneFlags) { f.workers = 0 },
		"bad backend":   func(f *pruneFlags) { f.backend = "badger" },
		"nothing to do": func(f *pruneFlags) { f.cosmosSDK, f.cometBFT = false, false },
	} {
		t.Run(name, func(t *testing.T) {
			f := defaultFlags()
			mutate(&f)
			require.Error(t, runPrune(context.Background(), dir, f))
		})
	}
	require.ErrorContains(t, runPrune(context.Background(), dir+"/nope", defaultFlags()), "not found")
}

func TestRunPruneReportsAppErrors(t *testing.T) {
	dir := writeNode(t, cpdb.BackendGoLevelDB, cpdb.BackendGoLevelDB)
	appDB, _, err := cpdb.Open(dir, "application", cpdb.BackendAuto)
	require.NoError(t, err)
	testutil.WriteCommitInfo(t, appDB, 29, "bank") // multistore behind the tree
	require.NoError(t, appDB.Close())

	err = runPrune(context.Background(), dir, defaultFlags())
	require.ErrorContains(t, err, "store bank")
}

func TestRunPruneRefusesBeforeDeletingAnything(t *testing.T) {
	dir := writeNode(t, cpdb.BackendGoLevelDB, cpdb.BackendGoLevelDB)
	appDB, _, err := cpdb.Open(dir, "application", cpdb.BackendAuto)
	require.NoError(t, err)
	testutil.WriteLegacyRoot(t, appDB, "gov", 30)
	testutil.WriteCommitInfo(t, appDB, 30, "bank", "gov")
	require.NoError(t, appDB.Close())

	err = runPrune(context.Background(), dir, defaultFlags())
	require.ErrorIs(t, err, app.ErrLegacyIAVL)

	blockDB, _, err := cpdb.Open(dir, "blockstore", cpdb.BackendAuto)
	require.NoError(t, err)
	defer testutil.CloseDB(t, blockDB)
	require.EqualValues(t, 1, store.NewBlockStore(cpdb.CometBFT{DB: blockDB}).Base())
}
