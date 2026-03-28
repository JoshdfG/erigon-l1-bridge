.PHONY: build run test lint tidy vet abigen-clearing abigen-paymaster

BINARY = bin/bridge
CMD    = ./cmd/bridge

build:
	go build -o $(BINARY) $(CMD)

run:
	go run $(CMD)

test:
	go test ./... -v -count=1

vet:
	go vet ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

# Regenerate after modifying Solidity contracts
# Prereq: go install github.com/ethereum/go-ethereum/cmd/abigen@latest
abigen-clearing:
	abigen \
	  --abi internal/contracts/clearing/abi.json \
	  --pkg  clearing \
	  --out  internal/contracts/clearing/clearing.go

abigen-paymaster:
	abigen \
	  --abi internal/contracts/paymaster/abi.json \
	  --pkg  paymaster \
	  --out  internal/contracts/paymaster/paymaster.go
