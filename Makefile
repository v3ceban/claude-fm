BIN := claude-fm
GOFILES := $(shell find . -name '*.go' -not -path './.git/*')
GOPLS := $(shell command -v gopls 2>/dev/null || echo go run golang.org/x/tools/gopls@latest)

.PHONY: build run install uninstall test test-short lint fmt clean

build:
	go build -o $(BIN) .

run: build
	./$(BIN)

PREFIX ?= $(HOME)/.local

install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BIN) $(PREFIX)/bin/$(BIN)

uninstall:
	rm -f $(PREFIX)/bin/$(BIN)

test:
	go test -count=1 ./...

test-short:
	go test -short -count=1 ./...

lint:
	go vet ./...
	$(GOPLS) check $(GOFILES)

fmt:
	gofmt -l -w .

clean:
	rm -f $(BIN)
