.PHONY: build clean build-arm build-host dist fmt deps lint test

BINARY_NAME=data-server
BUILD_DIR=bin
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
VERSION_FLAGS=-X main.version=$(VERSION)
LDFLAGS=-ldflags "-w -s -extldflags '-static' $(VERSION_FLAGS)"
CMD_DIR=cmd/data-server

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

build-arm: build

build-host:
	mkdir -p $(BUILD_DIR)
	go build -trimpath -ldflags "$(VERSION_FLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

dist: build

test:
	GOTOOLCHAIN=go1.25.7 go test ./...

lint:
	golangci-lint run

fmt:
	go fmt ./...

deps:
	GOTOOLCHAIN=go1.25.7 go mod tidy

clean:
	rm -rf $(BUILD_DIR)
