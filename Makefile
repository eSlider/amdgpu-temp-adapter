# ryzenadj-governor - build/test/release helpers.
#
# The project is Linux-only and builds without cgo.

BINARY      := ryzenadj-governor
PKG         := github.com/eSlider/ryzenadj
CMD         := ./cmd/ryzenadj-governor
BIN_DIR     := bin
DIST_DIR    := dist
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.Date=$(DATE)

.PHONY: all build install test vet fmt fmt-check lint release-check clean

all: fmt-check vet test build

## build: compile the local binary into ./bin
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)

## install: go install the command
install:
	go install -trimpath -ldflags "$(LDFLAGS)" $(CMD)

## test: run the unit tests
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format the tree
fmt:
	gofmt -w .

## fmt-check: fail if anything is unformatted
fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

## lint: run gofmt check, vet and golangci-lint
lint: fmt-check vet
	golangci-lint run ./...

## release-check: reproduce the release artifacts locally (linux amd64+arm64)
release-check: clean
	@mkdir -p $(DIST_DIR)
	@for arch in amd64 arm64; do \
		out="$(DIST_DIR)/$(BINARY)_$(VERSION)_linux_$$arch"; \
		mkdir -p "$$out"; \
		GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out/$(BINARY)" $(CMD); \
		cp README.md LICENSE "$$out/"; \
		tar -C $(DIST_DIR) -czf "$$out.tar.gz" "$$(basename $$out)"; \
	done
	@(cd $(DIST_DIR) && sha256sum *.tar.gz > sha256sums.txt)
	@cat $(DIST_DIR)/sha256sums.txt

## clean: remove build output
clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
