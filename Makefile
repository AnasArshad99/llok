BIN=llok
MODULE=github.com/muhammadanas/llok
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build guest run test clean

build:
	go build -ldflags "-X $(MODULE)/internal/cli.Version=$(VERSION)" -o $(BIN) ./cmd/llok
	codesign --force --sign - --entitlements llok.entitlements $(BIN)

guest: build
	./$(BIN) guest build

run: build
	./$(BIN) up

test:
	go vet ./... && go test ./...

clean:
	rm -f $(BIN)
