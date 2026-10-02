.PHONY: run build fmt vet test check

run:
	go run ./cmd/api

build:
	mkdir -p bin
	go build -trimpath -o bin/pixiv-curator ./cmd/api

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

test:
	go test -race ./...

check: vet test
