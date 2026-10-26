// Package app prunes historical versions from a Cosmos SDK application database.
package app

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/iavl"
	"golang.org/x/sync/errgroup"

	cpdb "github.com/01builders/cosmprund/v2/internal/db"
)

// iavlCacheSize is the per-tree node cache. Pruning walks each version once,
// so a large cache only costs memory.
const iavlCacheSize = 10_000

// ErrLegacyIAVL is returned for stores written only in the pre-v1 IAVL format
// (Cosmos SDK v0.47 and earlier). IAVL v1 cannot partially prune such trees.
var ErrLegacyIAVL = errors.New("store uses the legacy IAVL format (Cosmos SDK <= v0.47); use cosmprund v1.x for this chain")

// Options configures application state pruning.
type Options struct {
	// KeepVersions is the number of most recent versions to keep. Must be >= 1.
	KeepVersions int64
	// Workers bounds the number of stores pruned concurrently. Must be >= 1.
	Workers int
	Logger  *slog.Logger
}

// StoreResult describes what happened to one IAVL store.
type StoreResult struct {
	Name string
	// PrunedTo is the highest deleted version, or 0 if nothing was deleted.
	PrunedTo int64
	Skipped  string
}

// Check runs every validation Prune performs without writing anything, so
// callers can refuse up front instead of leaving a node partially pruned.
func Check(ctx context.Context, db dbm.DB, opts Options) ([]StoreResult, error) {
	return run(ctx, db, opts, false)
}

// Prune deletes all but the most recent opts.KeepVersions versions from every
// IAVL store listed in the latest commit info. All stores are validated before
// any of them is modified.
func Prune(ctx context.Context, db dbm.DB, opts Options) ([]StoreResult, error) {
	if _, err := run(ctx, db, opts, false); err != nil {
		return nil, err
	}
	return run(ctx, db, opts, true)
}

func run(ctx context.Context, db dbm.DB, opts Options, apply bool) ([]StoreResult, error) {
	if opts.KeepVersions < 1 {
		return nil, fmt.Errorf("versions to keep must be at least 1, got %d", opts.KeepVersions)
	}
	if opts.Workers < 1 {
		return nil, fmt.Errorf("workers must be at least 1, got %d", opts.Workers)
	}

	latest, err := LatestVersion(db)
	if err != nil {
		return nil, err
	}
	if latest == 0 {
		return nil, errors.New("application database has no committed versions")
	}
	ci, err := ReadCommitInfo(db, latest)
	if err != nil {
		return nil, err
	}

	toVersion := latest - opts.KeepVersions
	if apply {
		opts.Logger.Info("pruning application state", "latest", latest, "keep", opts.KeepVersions, "stores", len(ci.StoreInfos))
	}

	results := make([]StoreResult, len(ci.StoreInfos))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Workers)
	for i, si := range ci.StoreInfos {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			res, err := pruneStore(db, si.Name, latest, toVersion, apply, opts.Logger)
			if err != nil {
				return fmt.Errorf("store %s: %w", si.Name, err)
			}
			results[i] = res
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

func pruneStore(db dbm.DB, name string, latest, toVersion int64, apply bool, logger *slog.Logger) (_ StoreResult, err error) {
	res := StoreResult{Name: name}
	sdb := dbm.NewPrefixDB(db, []byte("s/k:"+name+"/"))

	legacyLatest, err := legacyLatestVersion(sdb)
	if err != nil {
		return res, err
	}
	if legacyLatest > 0 {
		hasV1, err := hasKeyPrefix(sdb, 's')
		if err != nil {
			return res, err
		}
		if !hasV1 {
			return res, ErrLegacyIAVL
		}
	}

	tree := iavl.NewMutableTree(cpdb.IAVL{DB: sdb}, iavlCacheSize, true, logger.With("store", name))
	// Close flushes pending batches; it does not close the shared db.
	defer func() { err = errors.Join(err, tree.Close()) }()

	treeLatest, err := tree.LoadVersion(0)
	if err != nil {
		return res, fmt.Errorf("load tree: %w", err)
	}
	if treeLatest == 0 {
		// Memory or transient stores sometimes leak into commit info without
		// ever persisting data; there is nothing to prune.
		res.Skipped = "no persisted versions"
		return res, nil
	}
	if treeLatest != latest {
		return res, fmt.Errorf("tree is at version %d but the multistore committed %d; start the node once so it reconciles, then retry", treeLatest, latest)
	}

	if toVersion < 1 {
		res.Skipped = "fewer versions than requested to keep"
		return res, nil
	}
	if legacyLatest > toVersion {
		// IAVL v1 removes legacy versions all at once, only after they leave
		// the keep window. Deleting would be a silent no-op.
		res.Skipped = fmt.Sprintf("legacy IAVL versions up to %d are inside the keep window", legacyLatest)
		return res, nil
	}
	if !apply {
		return res, nil
	}

	logger.Info("pruning store", "store", name, "to", toVersion)
	if err := tree.DeleteVersionsTo(toVersion); err != nil {
		return res, fmt.Errorf("delete versions to %d: %w", toVersion, err)
	}
	res.PrunedTo = toVersion
	logger.Info("finished pruning store", "store", name)
	return res, nil
}

// legacyLatestVersion returns the newest version stored with a legacy
// (pre-IAVL v1) root key, r<int64 version>, or 0 if there is none.
func legacyLatestVersion(db dbm.DB) (v int64, err error) {
	it, err := db.ReverseIterator(legacyRootKey(1), legacyRootKey(math.MaxInt64))
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, it.Close()) }()

	if it.Valid() {
		k := it.Key()
		if len(k) != 9 {
			return 0, fmt.Errorf("malformed legacy root key %x", k)
		}
		v = int64(binary.BigEndian.Uint64(k[1:]))
	}
	return v, it.Error()
}

// hasKeyPrefix reports whether any key starts with byte b. IAVL v1 nodes are
// stored under s<version><nonce>; their absence means a store was never
// written by IAVL v1.
func hasKeyPrefix(db dbm.DB, b byte) (found bool, err error) {
	if b == 0xff {
		return false, errors.New("key prefix 0xff has no upper bound")
	}
	it, err := db.Iterator([]byte{b}, []byte{b + 1})
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, it.Close()) }()
	return it.Valid(), it.Error()
}

func legacyRootKey(version int64) []byte {
	k := make([]byte, 9)
	k[0] = 'r'
	binary.BigEndian.PutUint64(k[1:], uint64(version))
	return k
}
