SHELL := /bin/sh

.PHONY: preflight lock prepare build verify

preflight:
	@scripts/check-build-environment.sh

lock: preflight
	@go mod tidy

prepare: preflight
	@go mod download
	@go mod verify

build: prepare
	@go build ./...

verify: prepare
	@go mod tidy -diff
	@go test -count=1 ./...
	@go vet ./...
