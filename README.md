# cosmprund

cosmprund prunes a stopped Cosmos SDK / CometBFT node down to its most recent
blocks and application state versions, so you can reclaim disk space without
state syncing.

## Before you use it

- **Stop the node** and **back up the data directory**. Pruning deletes data
  and cannot be undone.
- Check whether you need this tool at all. Recent nodes can prune themselves:
  `pruning` in `app.toml`, `min-retain-blocks` in `app.toml`, and the
  `<appd> prune` command shipped with Cosmos SDK v0.47+.

## Compatibility

| Component | Supported |
| --- | --- |
| Application state | Cosmos SDK v0.50+ (IAVL v1), including chains upgraded from v0.47 |
| Consensus data | Tendermint v0.34, CometBFT v0.37, v0.38, and v1.x with the default key layout |
| Databases | goleveldb, pebbledb (detected automatically per database); rocksdb when built with `-tags rocksdb` |

Stores written only in the legacy IAVL format (Cosmos SDK v0.47 and earlier)
are refused with a clear error; use cosmprund v1.x for those chains. On chains
upgraded from v0.47, legacy versions are removed once they fall outside the keep
window. CometBFT v1 databases using the ordered `v2` key layout are refused.

Every database and store is validated before anything is deleted, so an
unsupported store aborts the run without modifying the node.

## Install

```bash
go install github.com/01builders/cosmprund/v2@latest
```

or from source:

```bash
git clone https://github.com/01builders/cosmprund
cd cosmprund
make build   # binary in build/cosmprund
```

## Usage

```bash
sudo systemctl stop cosmovisor
cosmprund prune ~/.gaiad/data --blocks 100 --versions 100
```

The argument is the node's `data` directory, containing `application.db`,
`blockstore.db` and `state.db`.

| Flag | Default | Description |
| --- | --- | --- |
| `-b, --blocks` | 10 | Recent blocks to keep |
| `-v, --versions` | 10 | Recent application state versions to keep |
| `--backend` | `auto` | `auto`, `goleveldb`, `pebbledb` or `rocksdb` |
| `--cosmos-sdk` | true | Prune application state; set `false` for non Cosmos SDK chains |
| `--tendermint` | true | Prune CometBFT/Tendermint blocks and state |
| `--compact` | true | Compact databases afterwards to reclaim disk space |
| `--workers` | 4 | Application stores pruned concurrently |

Block pruning follows CometBFT's own rules: headers and commits still inside the
chain's evidence window are kept, while block contents are removed.

## License

[Apache-2.0](LICENSE)
