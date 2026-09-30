BIN=llok
PREFIX?=$(HOME)/.local
MODULE=github.com/AnasArshad99/llok
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build guest run test clean install uninstall

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

# Installs a signed binary onto your PATH (default: ~/.local/bin).
install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/$(BIN)
	@echo "installed $(PREFIX)/bin/$(BIN)"
	@case ":$$PATH:" in *":$(PREFIX)/bin:"*) ;; *) echo "note: add $(PREFIX)/bin to your PATH";; esac

uninstall:
	rm -f $(PREFIX)/bin/$(BIN)
