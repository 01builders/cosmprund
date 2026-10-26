package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	cpdb "github.com/01builders/cosmprund/v2/internal/db"
	"github.com/01builders/cosmprund/v2/internal/testutil"
)

func TestCommitInfoRoundTrip(t *testing.T) {
	db := testutil.NewDB(t, t.TempDir(), "application", cpdb.BackendGoLevelDB)
	defer testutil.CloseDB(t, db)

	v, err := LatestVersion(db)
	require.NoError(t, err)
	require.Zero(t, v)

	testutil.WriteCommitInfo(t, db, 42, "bank", "wasm")
	v, err = LatestVersion(db)
	require.NoError(t, err)
	require.EqualValues(t, 42, v)

	ci, err := ReadCommitInfo(db, 42)
	require.NoError(t, err)
	require.EqualValues(t, 42, ci.Version)
	require.Equal(t, []StoreInfo{
		{Name: "bank", Version: 42, Hash: []byte("hash")},
		{Name: "wasm", Version: 42, Hash: []byte("hash")},
	}, ci.StoreInfos)

	_, err = ReadCommitInfo(db, 41)
	require.ErrorContains(t, err, "no commit info found")
}

func TestDecodeCommitInfoRejectsMalformed(t *testing.T) {
	for name, bz := range map[string][]byte{
		"truncated tag":       {0x80},
		"truncated varint":    {0x08, 0x80},
		"truncated length":    {0x12, 0x05, 0x01},
		"store without name":  {0x12, 0x00},
		"bad nested store id": {0x12, 0x04, 0x12, 0x02, 0x08, 0x80},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeCommitInfo(bz)
			require.Error(t, err)
		})
	}
}

func TestDecodeCommitInfoSkipsUnknownFields(t *testing.T) {
	// version=3, unknown field 3 (timestamp) as bytes, one store "a".
	bz := []byte{0x08, 0x03, 0x1a, 0x02, 0x08, 0x01, 0x12, 0x03, 0x0a, 0x01, 'a'}
	ci, err := decodeCommitInfo(bz)
	require.NoError(t, err)
	require.EqualValues(t, 3, ci.Version)
	require.Equal(t, []StoreInfo{{Name: "a"}}, ci.StoreInfos)
}
