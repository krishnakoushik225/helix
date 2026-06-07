.PHONY: build run gen-token

build:
	go build ./...

run:
	go run ./cmd/helix

gen-token:
	@set -a; . ./.env; set +a; go run ./cmd/gen-token
