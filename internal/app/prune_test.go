package app_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/iavl"
	"github.com/stretchr/testify/require"

	"github.com/01builders/cosmprund/v2/internal/app"
	cpdb "github.com/01builders/cosmprund/v2/internal/db"
	"github.com/01builders/cosmprund/v2/internal/testutil"
)

var backends = []cpdb.Backend{cpdb.BackendGoLevelDB, cpdb.BackendPebbleDB}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func opts(keep int64) app.Options {
	return app.Options{KeepVersions: keep, Workers: 2, Logger: discard()}
}

func openTree(t *testing.T, db dbm.DB, name string) *iavl.MutableTree {
	t.Helper()
	sdb := dbm.NewPrefixDB(db, []byte("s/k:"+name+"/"))
	tree := iavl.NewMutableTree(cpdb.IAVL{DB: sdb}, 100, true, iavl.NewNopLogger())
	_, err := tree.LoadVersion(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tree.Close() })
	return tree
}

func TestPruneKeepsRecentVersions(t *testing.T) {
	for _, backend := range backends {
		t.Run(string(backend), func(t *testing.T) {
			db := testutil.NewDB(t, t.TempDir(), "application", backend)
			defer testutil.CloseDB(t, db)

			// "late" models a store added by an upgrade: it has fewer versions.
			testutil.WriteAppStore(t, db, "bank", 20)
			testutil.WriteAppStore(t, db, "staking", 20)
			late := dbm.NewPrefixDB(db, []byte("s/k:late/"))
			tree := iavl.NewMutableTree(cpdb.IAVL{DB: late}, 100, true, iavl.NewNopLogger())
			tree.SetInitialVersion(12)
			for v := int64(12); v <= 20; v++ {
				_, err := tree.Set(testutil.StoreKey(v), []byte("late"))
				require.NoError(t, err)
				_, _, err = tree.SaveVersion()
				require.NoError(t, err)
			}
			require.NoError(t, tree.Close())
			testutil.WriteCommitInfo(t, db, 20, "bank", "staking", "late")

			results, err := app.Prune(context.Background(), db, opts(5))
			require.NoError(t, err)
			require.Len(t, results, 3)
			for _, r := range results {
				require.Empty(t, r.Skipped, r.Name)
				require.EqualValues(t, 15, r.PrunedTo, r.Name)
			}

			for _, name := range []string{"bank", "staking", "late"} {
				tree := openTree(t, db, name)
				for v := int64(1); v <= 20; v++ {
					require.Equal(t, v > 15, tree.VersionExists(v), "%s version %d", name, v)
				}
				// Latest state still contains keys written at pruned versions.
				val, err := tree.Get(testutil.StoreKey(14))
				require.NoError(t, err)
				require.Equal(t, []byte(name), val)
			}
		})
	}
}

func TestPruneIsIdempotent(t *testing.T) {
	db := testutil.NewDB(t, t.TempDir(), "application", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, db)
	testutil.WriteAppStore(t, db, "bank", 10)
	testutil.WriteCommitInfo(t, db, 10, "bank")

	_, err := app.Prune(context.Background(), db, opts(3))
	require.NoError(t, err)
	_, err = app.Prune(context.Background(), db, opts(3))
	require.NoError(t, err)
	require.True(t, openTree(t, db, "bank").VersionExists(8))
	require.False(t, openTree(t, db, "bank").VersionExists(7))
}

func TestPruneSkips(t *testing.T) {
	db := testutil.NewDB(t, t.TempDir(), "application", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, db)
	testutil.WriteAppStore(t, db, "bank", 4)
	testutil.WriteCommitInfo(t, db, 4, "bank", "memstore")

	results, err := app.Prune(context.Background(), db, opts(10))
	require.NoError(t, err)
	require.Equal(t, "fewer versions than requested to keep", results[0].Skipped)
	require.Equal(t, "no persisted versions", results[1].Skipped)
	require.True(t, openTree(t, db, "bank").VersionExists(1))
}

func TestPruneRejectsLegacyStore(t *testing.T) {
	db := testutil.NewDB(t, t.TempDir(), "application", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, db)
	testutil.WriteLegacyRoot(t, db, "bank", 7)
	testutil.WriteCommitInfo(t, db, 7, "bank")

	_, err := app.Prune(context.Background(), db, opts(2))
	require.ErrorIs(t, err, app.ErrLegacyIAVL)
}

func TestPruneRejectsTreeAheadOfCommitInfo(t *testing.T) {
	db := testutil.NewDB(t, t.TempDir(), "application", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, db)
	testutil.WriteAppStore(t, db, "bank", 10)
	testutil.WriteCommitInfo(t, db, 9, "bank")

	_, err := app.Prune(context.Background(), db, opts(2))
	require.ErrorContains(t, err, "tree is at version 10 but the multistore committed 9")
	require.True(t, openTree(t, db, "bank").VersionExists(1))
}

func TestPruneValidatesInput(t *testing.T) {
	db := testutil.NewDB(t, t.TempDir(), "application", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, db)

	_, err := app.Prune(context.Background(), db, opts(0))
	require.ErrorContains(t, err, "at least 1")
	_, err = app.Prune(context.Background(), db, opts(1))
	require.ErrorContains(t, err, "no committed versions")
}
