package db_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	cpdb "github.com/01builders/cosmprund/v2/internal/db"
	"github.com/01builders/cosmprund/v2/internal/testutil"
)

func TestDetectAndOpen(t *testing.T) {
	for _, backend := range []cpdb.Backend{cpdb.BackendGoLevelDB, cpdb.BackendPebbleDB} {
		t.Run(string(backend), func(t *testing.T) {
			dir := t.TempDir()
			db := testutil.NewDB(t, dir, "application", backend)
			for i := range byte(100) {
				require.NoError(t, db.Set([]byte{'k', i}, []byte{i}))
			}
			require.NoError(t, db.Close())

			got, err := cpdb.DetectBackend(filepath.Join(dir, "application.db"))
			require.NoError(t, err)
			require.Equal(t, backend, got)

			db, opened, err := cpdb.Open(dir, "application", cpdb.BackendAuto)
			require.NoError(t, err)
			defer testutil.CloseDB(t, db)
			require.Equal(t, backend, opened)

			for i := range byte(50) {
				require.NoError(t, db.Delete([]byte{'k', i}))
			}
			require.NoError(t, cpdb.Compact(db))
			v, err := db.Get([]byte{'k', 99})
			require.NoError(t, err)
			require.Equal(t, []byte{99}, v)
		})
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := cpdb.Open(dir, "missing", cpdb.BackendAuto)
	require.Error(t, err)

	require.NoError(t, os.Mkdir(filepath.Join(dir, "junk.db"), 0o755))
	_, _, err = cpdb.Open(dir, "junk", cpdb.BackendAuto)
	require.ErrorContains(t, err, "cannot detect database backend")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "junk.db", "IDENTITY"), nil, 0o644))
	_, _, err = cpdb.Open(dir, "junk", cpdb.BackendAuto)
	require.ErrorContains(t, err, "-tags rocksdb")
}

func TestCompactEmpty(t *testing.T) {
	db := testutil.NewDB(t, t.TempDir(), "empty", cpdb.BackendPebbleDB)
	defer testutil.CloseDB(t, db)
	require.NoError(t, cpdb.Compact(db))
}

func TestParseBackend(t *testing.T) {
	b, err := cpdb.ParseBackend("PebbleDB")
	require.NoError(t, err)
	require.Equal(t, cpdb.BackendPebbleDB, b)
	_, err = cpdb.ParseBackend("badger")
	require.Error(t, err)
}
