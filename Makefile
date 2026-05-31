.PHONY: build test clean lint

BINARY_NAME=foo
BIN_DIR=bin

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY_NAME) main.go

test: build
	go test -v ./...
	go test -v ./tests/e2e/...

clean:
	rm -rf $(BIN_DIR)
	go clean

lint:
	go vet ./...
	@command -v actionlint >/dev/null 2>&1 && actionlint -color || echo "actionlint not installed; skipping"
