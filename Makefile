BIN_DIR := bin
TOOLS_DIR := tools
GOLANGCI_LINT := $(BIN_DIR)/golangci-lint

.PHONY: all build build-xnp build-npgraph vet lint test check clean

all: build

build: build-xnp build-npgraph

build-xnp:
	go build -o $(BIN_DIR)/xnp ./cmd/xnp

build-npgraph:
	go build -o $(BIN_DIR)/npgraph ./cmd/npgraph

vet:
	go vet ./...

# golangci-lint is pinned in tools/go.mod, a separate module so its large
# dependency tree doesn't pollute the main module's go.sum. Built once into
# bin/, rebuilt only when the pin changes.
$(GOLANGCI_LINT): $(TOOLS_DIR)/go.mod $(TOOLS_DIR)/go.sum
	cd $(TOOLS_DIR) && go build -o ../$(GOLANGCI_LINT) github.com/golangci/golangci-lint/v2/cmd/golangci-lint

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run

test:
	go test ./...

check: vet lint test

clean:
	rm -rf $(BIN_DIR)
