BIN     := bin/dashboard
VERSION ?= dev

# version currently lives in package main; moves to internal/version later.
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build run test clean

build:
	@mkdir -p $(dir $(BIN))
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/dashboard

run: build
	./$(BIN) serve

test:
	go test ./...

clean:
	rm -f $(BIN)
