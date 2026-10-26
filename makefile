VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git log -1 --format='%H' 2>/dev/null)

LD_FLAGS = -X github.com/01builders/cosmprund/v2/cmd.Version=$(VERSION) \
	-X github.com/01builders/cosmprund/v2/cmd.Commit=$(COMMIT)

BUILD_FLAGS := -mod=readonly -trimpath -ldflags '$(LD_FLAGS)'

all: build

build:
	@echo "Building cosmprund"
	@go build $(BUILD_FLAGS) -o build/cosmprund .

install:
	@echo "Installing cosmprund"
	@go install $(BUILD_FLAGS) .

# RocksDB support needs CGO and a system librocksdb.
build-rocksdb:
	@go build $(BUILD_FLAGS) -tags rocksdb -o build/cosmprund .

test:
	@go test -race ./...

lint:
	@go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...

vulncheck:
	@go run golang.org/x/vuln/cmd/govulncheck@latest ./...

clean:
	rm -rf build

.PHONY: all build install build-rocksdb test lint vulncheck clean
