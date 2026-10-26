package app

import (
	"errors"
	"fmt"
	"math"

	dbm "github.com/cosmos/cosmos-db"
	"google.golang.org/protobuf/encoding/protowire"
)

// Keys written by the Cosmos SDK root multistore (store/rootmulti).
const (
	latestVersionKey = "s/latest"
	commitInfoKeyFmt = "s/%d"
)

// skipField tells walkFields to skip a field. It must not collide with the
// negative error codes returned by protowire.
const skipField = math.MinInt

// StoreInfo is the subset of cosmos.store.v1beta1.StoreInfo needed for pruning.
type StoreInfo struct {
	Name    string
	Version int64
	Hash    []byte
}

// CommitInfo is the subset of cosmos.store.v1beta1.CommitInfo needed for pruning.
type CommitInfo struct {
	Version    int64
	StoreInfos []StoreInfo
}

// LatestVersion returns the last version committed by the root multistore, or
// 0 if the database has no commits.
func LatestVersion(db dbm.DB) (int64, error) {
	bz, err := db.Get([]byte(latestVersionKey))
	if err != nil {
		return 0, fmt.Errorf("read latest version: %w", err)
	}
	if bz == nil {
		return 0, nil
	}
	// gogoproto Int64Value { int64 value = 1; }
	var v int64
	err = walkFields(bz, func(num protowire.Number, typ protowire.Type, b []byte) (int, error) {
		if num == 1 && typ == protowire.VarintType {
			x, n := protowire.ConsumeVarint(b)
			v = int64(x)
			return n, nil
		}
		return skipField, nil
	})
	if err != nil {
		return 0, fmt.Errorf("decode latest version: %w", err)
	}
	return v, nil
}

// ReadCommitInfo loads the commit info stored for version ver.
func ReadCommitInfo(db dbm.DB, ver int64) (CommitInfo, error) {
	bz, err := db.Get(fmt.Appendf(nil, commitInfoKeyFmt, ver))
	if err != nil {
		return CommitInfo{}, fmt.Errorf("read commit info %d: %w", ver, err)
	}
	if bz == nil {
		return CommitInfo{}, fmt.Errorf("no commit info found for version %d", ver)
	}
	ci, err := decodeCommitInfo(bz)
	if err != nil {
		return CommitInfo{}, fmt.Errorf("decode commit info %d: %w", ver, err)
	}
	return ci, nil
}

func decodeCommitInfo(bz []byte) (CommitInfo, error) {
	var ci CommitInfo
	err := walkFields(bz, func(num protowire.Number, typ protowire.Type, b []byte) (int, error) {
		switch {
		case num == 1 && typ == protowire.VarintType:
			x, n := protowire.ConsumeVarint(b)
			ci.Version = int64(x)
			return n, nil
		case num == 2 && typ == protowire.BytesType:
			msg, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return n, nil
			}
			si, err := decodeStoreInfo(msg)
			if err != nil {
				return 0, err
			}
			ci.StoreInfos = append(ci.StoreInfos, si)
			return n, nil
		}
		return skipField, nil
	})
	return ci, err
}

func decodeStoreInfo(bz []byte) (StoreInfo, error) {
	var si StoreInfo
	err := walkFields(bz, func(num protowire.Number, typ protowire.Type, b []byte) (int, error) {
		if typ != protowire.BytesType || (num != 1 && num != 2) {
			return skipField, nil
		}
		v, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return n, nil
		}
		if num == 1 {
			si.Name = string(v)
			return n, nil
		}
		// CommitID { int64 version = 1; bytes hash = 2; }
		err := walkFields(v, func(num protowire.Number, typ protowire.Type, b []byte) (int, error) {
			switch {
			case num == 1 && typ == protowire.VarintType:
				x, n := protowire.ConsumeVarint(b)
				si.Version = int64(x)
				return n, nil
			case num == 2 && typ == protowire.BytesType:
				h, n := protowire.ConsumeBytes(b)
				si.Hash = append([]byte(nil), h...)
				return n, nil
			}
			return skipField, nil
		})
		return n, err
	})
	if err == nil && si.Name == "" {
		err = errors.New("store info without name")
	}
	return si, err
}

// walkFields iterates over the top-level fields of a protobuf message. For each
// field, fn receives the bytes following the tag and returns how many it
// consumed, or skipField to skip it. Each iteration consumes at least one
// byte, so the loop is bounded by len(bz).
func walkFields(bz []byte, fn func(protowire.Number, protowire.Type, []byte) (int, error)) error {
	for len(bz) > 0 {
		num, typ, n := protowire.ConsumeTag(bz)
		if n < 0 {
			return protowire.ParseError(n)
		}
		bz = bz[n:]

		m, err := fn(num, typ, bz)
		if err != nil {
			return err
		}
		if m == skipField {
			m = protowire.ConsumeFieldValue(num, typ, bz)
		}
		if m < 0 {
			return protowire.ParseError(m)
		}
		bz = bz[m:]
	}
	return nil
}
