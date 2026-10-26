// Package db opens node databases with the correct backend and adapts them to
// the interfaces expected by IAVL and CometBFT.
package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corestore "cosmossdk.io/core/store"
	cmtdb "github.com/cometbft/cometbft-db"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Backend identifies an on-disk key/value store implementation.
type Backend string

const (
	BackendAuto      Backend = "auto"
	BackendGoLevelDB Backend = Backend(dbm.GoLevelDBBackend)
	BackendPebbleDB  Backend = Backend(dbm.PebbleDBBackend)
	BackendRocksDB   Backend = Backend(dbm.RocksDBBackend)
)

// ParseBackend validates a user-supplied backend name.
func ParseBackend(s string) (Backend, error) {
	switch b := Backend(strings.ToLower(s)); b {
	case BackendAuto, BackendGoLevelDB, BackendPebbleDB, BackendRocksDB:
		return b, nil
	default:
		return "", fmt.Errorf("unknown backend %q (want auto, goleveldb, pebbledb or rocksdb)", s)
	}
}

// DetectBackend inspects the files in an existing database directory to
// determine which engine wrote it. Opening a database with the wrong engine
// either fails or, for goleveldb reading a Pebble manifest, panics.
func DetectBackend(path string) (Backend, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("read database directory: %w", err)
	}

	var hasCurrent, hasOptions, hasIdentity bool
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "marker."):
			// Pebble-only: format-version and manifest markers.
			return BackendPebbleDB, nil
		case name == "IDENTITY":
			hasIdentity = true
		case strings.HasPrefix(name, "OPTIONS-"):
			hasOptions = true
		case name == "CURRENT":
			hasCurrent = true
		}
	}

	switch {
	case hasIdentity:
		return BackendRocksDB, nil
	case hasOptions:
		return BackendPebbleDB, nil
	case hasCurrent:
		return BackendGoLevelDB, nil
	default:
		return "", fmt.Errorf("cannot detect database backend in %s; pass --backend explicitly", path)
	}
}

// Open opens the existing database dir/name.db. With BackendAuto the backend is
// detected from the files on disk. Open never creates a new database.
func Open(dir, name string, backend Backend) (dbm.DB, Backend, error) {
	path := filepath.Join(dir, name+".db")
	if _, err := os.Stat(path); err != nil {
		return nil, "", fmt.Errorf("database %s: %w", path, err)
	}

	if backend == BackendAuto {
		detected, err := DetectBackend(path)
		if err != nil {
			return nil, "", err
		}
		backend = detected
	}

	var (
		db  dbm.DB
		err error
	)
	switch backend {
	case BackendGoLevelDB:
		// Seek-triggered compactions only slow down a bulk delete workload; we
		// compact explicitly once pruning is done.
		db, err = dbm.NewGoLevelDBWithOpts(name, dir, &opt.Options{DisableSeeksCompaction: true})
	default:
		db, err = dbm.NewDB(name, dbm.BackendType(backend), dir)
	}
	if err != nil {
		if backend == BackendRocksDB {
			err = fmt.Errorf("%w (rocksdb requires building with -tags rocksdb)", err)
		}
		return nil, "", fmt.Errorf("open %s (%s): %w", path, backend, err)
	}
	return db, backend, nil
}

// Compact reclaims the disk space freed by pruning.
func Compact(db dbm.DB) error {
	switch d := db.(type) {
	case *dbm.GoLevelDB:
		return d.ForceCompact(nil, nil)
	case *dbm.PebbleDB:
		return compactPebble(d)
	default:
		return fmt.Errorf("compaction not supported for %T", db)
	}
}

func compactPebble(d *dbm.PebbleDB) error {
	first, last, err := keyRange(d)
	if err != nil || first == nil {
		return err
	}
	// Pebble's end bound is exclusive; extend past the last key.
	end := append(append([]byte{}, last...), 0x00)
	return d.DB().Compact(first, end, true)
}

func keyRange(db dbm.DB) (first, last []byte, err error) {
	it, err := db.Iterator(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	if it.Valid() {
		first = append([]byte{}, it.Key()...)
	}
	if err := errors.Join(it.Error(), it.Close()); err != nil || first == nil {
		return nil, nil, err
	}

	rit, err := db.ReverseIterator(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	if rit.Valid() {
		last = append([]byte{}, rit.Key()...)
	}
	if err := errors.Join(rit.Error(), rit.Close()); err != nil {
		return nil, nil, err
	}
	return first, last, nil
}

// IAVL adapts a cosmos-db database to the interface used by cosmos/iavl v1.
type IAVL struct{ dbm.DB }

func (w IAVL) Iterator(start, end []byte) (corestore.Iterator, error) {
	return w.DB.Iterator(start, end)
}

func (w IAVL) ReverseIterator(start, end []byte) (corestore.Iterator, error) {
	return w.DB.ReverseIterator(start, end)
}

func (w IAVL) NewBatch() corestore.Batch { return w.DB.NewBatch() }

func (w IAVL) NewBatchWithSize(size int) corestore.Batch { return w.DB.NewBatchWithSize(size) }

// CometBFT adapts a cosmos-db database to the cometbft-db interface. Using a
// single database library keeps backend support (notably Pebble, which
// cometbft-db only builds behind a tag) identical for app and consensus data.
type CometBFT struct{ dbm.DB }

var _ cmtdb.DB = CometBFT{}

func (w CometBFT) Iterator(start, end []byte) (cmtdb.Iterator, error) {
	return w.DB.Iterator(start, end)
}

func (w CometBFT) ReverseIterator(start, end []byte) (cmtdb.Iterator, error) {
	return w.DB.ReverseIterator(start, end)
}

func (w CometBFT) NewBatch() cmtdb.Batch { return w.DB.NewBatch() }

func (w CometBFT) Compact(_, _ []byte) error { return Compact(w.DB) }
