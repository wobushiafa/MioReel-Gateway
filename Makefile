SERVER_DIR := server
BIN_DIR    := bin
BINARY     := $(BIN_DIR)/media-gateway

.PHONY: all build build-linux package-linux test test-race vet fmt tidy run golden clean

all: vet test-race build

build:
	@mkdir -p $(BIN_DIR)
	cd $(SERVER_DIR) && go build -trimpath -o ../$(BINARY) ./cmd/gateway

build-linux:
	@mkdir -p $(BIN_DIR)
	cd $(SERVER_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../$(BIN_DIR)/media-gateway-linux-amd64 ./cmd/gateway

package-linux:
	bash scripts/package-linux.sh

test:
	cd $(SERVER_DIR) && go test -count=1 ./...

test-race:
	cd $(SERVER_DIR) && go test -count=1 -race ./...

vet:
	cd $(SERVER_DIR) && go vet ./...

fmt:
	cd $(SERVER_DIR) && gofmt -w .

tidy:
	cd $(SERVER_DIR) && go mod tidy

run: build
	./$(BINARY)

golden:
	node tools/golden/recorder.js tools/golden/out

clean:
	rm -rf $(BIN_DIR) dist coverage.out
