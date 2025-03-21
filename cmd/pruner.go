package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/cosmos/cosmos-sdk/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/neilotoole/errgroup"
	"github.com/spf13/cobra"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/tendermint/tendermint/state"
	tmstore "github.com/tendermint/tendermint/store"
	db "github.com/tendermint/tm-db"

	"github.com/binaryholdings/cosmos-pruner/internal/rootmulti"
)

// load db
// load app store and prune
// if immutable tree is not deletable we should import and export current state

func pruneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune [path_to_home]",
		Short: "prune data from the application store and block store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {

			ctx := cmd.Context()
			errs, _ := errgroup.WithContext(ctx)
			var err error
			if tendermint {
				errs.Go(func() error {
					if err = pruneTMData(args[0]); err != nil {
						return err
					}
					return nil
				})
			}

			if cosmosSdk {
				err = pruneAppState(args[0])
				if err != nil {
					return err
				}
				return nil

			}

			return errs.Wait()
		},
	}
	return cmd
}

func pruneAppState(home string) error {

	// this has the potential to expand size, should just use state sync
	// dbType := db.BackendType(backend)

	dbDir := rootify(dataDir, home)

	o := opt.Options{
		DisableSeeksCompaction: true,
	}

	// Get BlockStore
	appDB, err := db.NewGoLevelDBWithOpts("application", dbDir, &o)
	if err != nil {
		return err
	}

	//TODO: need to get all versions in the store, setting randomly is too slow
	fmt.Println("pruning application state")

	appStore := rootmulti.NewStore(appDB)
	ver := rootmulti.GetLatestVersion(appDB)

	// find all available keys in database
	storeNames := []string{}
	if ver != 0 {
		cInfo, err := appStore.GetCommitInfo(ver)
		if err != nil {
			return err
		}

		for _, storeInfo := range cInfo.StoreInfos {
			// we only want to prune the stores with actual data.
			// sometimes in-memory stores get leaked to disk without data.
			// if that happens, the store's computed hash is empty as well.
			if len(storeInfo.CommitId.Hash) > 0 {
				storeNames = append(storeNames, storeInfo.Name)
			} else {
				fmt.Println("skipping", storeInfo.Name, "store due to empty hash")
			}
		}
	}

	keys := types.NewKVStoreKeys(storeNames...)
	for _, value := range keys {
		appStore.MountStoreWithDB(value, sdk.StoreTypeIAVL, nil)
	}

	err = appStore.LoadLatestVersion()
	if err != nil {
		return err
	}

	versions := appStore.GetAllVersions()

	v64 := make([]int64, len(versions))
	for i := 0; i < len(versions); i++ {
		v64[i] = int64(versions[i])
	}

	fmt.Println(len(v64))

	// Keep at least the last 10 versions, or all versions if less than 10 exist
	if len(v64) > 10 {
		appStore.PruneHeights = v64[:len(v64)-10]
	} else {
		// If we have 10 or fewer versions, don't prune any
		appStore.PruneHeights = []int64{}
	}

	appStore.PruneStores()

	fmt.Println("compacting application state")
	if err := appDB.ForceCompact(nil, nil); err != nil {
		return err
	}

	//create a new app store
	return nil
}

// pruneTMData prunes the tendermint blocks and state based on the amount of blocks to keep
func pruneTMData(home string) error {

	dbDir := rootify(dataDir, home)

	o := opt.Options{
		DisableSeeksCompaction: true,
	}

	// Get BlockStore
	blockStoreDB, err := db.NewGoLevelDBWithOpts("blockstore", dbDir, &o)
	if err != nil {
		return err
	}
	blockStore := tmstore.NewBlockStore(blockStoreDB)

	// Get StateStore
	stateDB, err := db.NewGoLevelDBWithOpts("state", dbDir, &o)
	if err != nil {
		return err
	}

	stateStore := state.NewStore(stateDB)

	base := blockStore.Base()

	pruneHeight := blockStore.Height() - int64(blocks)

	wg := sync.WaitGroup{}
	wg.Add(1)
	errs, _ := errgroup.WithContext(context.Background())
	errs.Go(func() error {
		fmt.Println("pruning block store")
		// prune block store
		blocks, err = blockStore.PruneBlocks(pruneHeight)
		if err != nil {
			return err
		}

		fmt.Println("compacting block store")
		if err := blockStoreDB.ForceCompact(nil, nil); err != nil {
			return err
		}

		wg.Done()
		return nil
	})

	fmt.Println("pruning state store")
	// prune state store
	err = stateStore.PruneStates(base, pruneHeight)
	if err != nil {
		return err
	}

	fmt.Println("compacting state store")
	if err := stateDB.ForceCompact(nil, nil); err != nil {
		return err
	}

	wg.Wait()

	stateDB.Close()
	blockStore.Close()

	return nil
}

// Utils

func rootify(path, root string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}
