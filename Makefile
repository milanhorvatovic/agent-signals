.PHONY: fmt lint test build check

fmt:
	golangci-lint fmt ./...

lint:
	golangci-lint run ./...

test:
	go test ./...

build:
	go build ./...

check: lint test build
