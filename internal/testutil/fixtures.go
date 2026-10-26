// Package testutil builds small on-disk node databases for tests.
package testutil

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/iavl"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	cpdb "github.com/01builders/cosmprund/v2/internal/db"
)

// ChainID is used for all generated CometBFT data.
const ChainID = "cosmprund-test"

// NewDB creates an empty database dir/name.db.
func NewDB(t testing.TB, dir, name string, backend cpdb.Backend) dbm.DB {
	t.Helper()
	db, err := dbm.NewDB(name, dbm.BackendType(backend), dir)
	require.NoError(t, err)
	return db
}

// StoreKey returns the key under which version v of a test store holds data.
func StoreKey(v int64) []byte { return fmt.Appendf(nil, "key-%d", v) }

// WriteAppStore commits versions 1..versions of an IAVL v1 store named name
// into db, writing one new key per version.
func WriteAppStore(t testing.TB, db dbm.DB, name string, versions int64) {
	t.Helper()
	sdb := dbm.NewPrefixDB(db, []byte("s/k:"+name+"/"))
	tree := iavl.NewMutableTree(cpdb.IAVL{DB: sdb}, 100, true, iavl.NewNopLogger())
	for v := int64(1); v <= versions; v++ {
		_, err := tree.Set(StoreKey(v), []byte(name))
		require.NoError(t, err)
		_, saved, err := tree.SaveVersion()
		require.NoError(t, err)
		require.Equal(t, v, saved)
	}
	require.NoError(t, tree.Close())
}

// WriteCommitInfo writes the root multistore metadata for version ver.
func WriteCommitInfo(t testing.TB, db dbm.DB, ver int64, stores ...string) {
	t.Helper()
	var ci []byte
	ci = protowire.AppendTag(ci, 1, protowire.VarintType)
	ci = protowire.AppendVarint(ci, uint64(ver))
	for _, name := range stores {
		var cid []byte
		cid = protowire.AppendTag(cid, 1, protowire.VarintType)
		cid = protowire.AppendVarint(cid, uint64(ver))
		cid = protowire.AppendTag(cid, 2, protowire.BytesType)
		cid = protowire.AppendBytes(cid, []byte("hash"))

		var si []byte
		si = protowire.AppendTag(si, 1, protowire.BytesType)
		si = protowire.AppendString(si, name)
		si = protowire.AppendTag(si, 2, protowire.BytesType)
		si = protowire.AppendBytes(si, cid)

		ci = protowire.AppendTag(ci, 2, protowire.BytesType)
		ci = protowire.AppendBytes(ci, si)
	}
	require.NoError(t, db.Set(fmt.Appendf(nil, "s/%d", ver), ci))

	var latest []byte
	latest = protowire.AppendTag(latest, 1, protowire.VarintType)
	latest = protowire.AppendVarint(latest, uint64(ver))
	require.NoError(t, db.Set([]byte("s/latest"), latest))
}

// WriteLegacyRoot writes a pre-IAVL v1 root key for version v in store name.
func WriteLegacyRoot(t testing.TB, db dbm.DB, name string, v int64) {
	t.Helper()
	k := make([]byte, 9)
	k[0] = 'r'
	binary.BigEndian.PutUint64(k[1:], uint64(v))
	require.NoError(t, db.Set(append([]byte("s/k:"+name+"/"), k...), []byte("legacy-root-hash")))
}

// WriteChain saves blocks 1..height to blockDB and the matching consensus
// state to stateDB. With expireEvidence, every block but the latest is older
// than the evidence window, so CometBFT prunes headers and commits too.
func WriteChain(t testing.TB, blockDB, stateDB dbm.DB, height int64, expireEvidence bool) {
	t.Helper()
	genesisTime := time.Now().Add(-time.Duration(height+1) * time.Hour).UTC()
	params := types.DefaultConsensusParams()
	if expireEvidence {
		params.Evidence.MaxAgeNumBlocks = 1
		params.Evidence.MaxAgeDuration = time.Nanosecond
	}
	pub := ed25519.GenPrivKey().PubKey()
	state, err := sm.MakeGenesisState(&types.GenesisDoc{
		ChainID:         ChainID,
		GenesisTime:     genesisTime,
		InitialHeight:   1,
		ConsensusParams: params,
		Validators:      []types.GenesisValidator{{Address: pub.Address(), PubKey: pub, Power: 10}},
	})
	require.NoError(t, err)

	bs := store.NewBlockStore(cpdb.CometBFT{DB: blockDB})
	ss := sm.NewStore(cpdb.CometBFT{DB: stateDB}, sm.StoreOptions{})
	require.NoError(t, ss.Save(state))

	lastCommit := &types.Commit{}
	for h := int64(1); h <= height; h++ {
		block := types.MakeBlock(h, nil, lastCommit, nil)
		block.ChainID = ChainID
		block.Time = genesisTime.Add(time.Duration(h) * time.Hour)
		block.ProposerAddress = pub.Address()
		parts, err := block.MakePartSet(types.BlockPartSizeBytes)
		require.NoError(t, err)
		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		seen := &types.Commit{Height: h, BlockID: blockID, Signatures: []types.CommitSig{{
			BlockIDFlag:      types.BlockIDFlagCommit,
			ValidatorAddress: pub.Address(),
			Timestamp:        block.Time,
			Signature:        make([]byte, ed25519.SignatureSize), // never verified
		}}}
		bs.SaveBlock(block, parts, seen)

		state.LastBlockHeight = h
		state.LastBlockID = blockID
		state.LastBlockTime = block.Time
		state.LastValidators = state.Validators.Copy()
		require.NoError(t, ss.Save(state))
		lastCommit = seen
	}
}

// CloseDB closes db and fails the test on error. Intended for defer.
func CloseDB(t testing.TB, db dbm.DB) {
	t.Helper()
	require.NoError(t, db.Close())
}
