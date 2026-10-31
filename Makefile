.PHONY: check fmt-check protocol-check contract-check

# Static contract checks do not execute task behavior or prove service reliability.
check: fmt-check protocol-check contract-check
	go test ./tools/check-contract
	go vet ./...
	go build ./...

fmt-check:
	test -z "$$(gofmt -l internal tools)"

protocol-check:
	cd protocol/upstream && sha256sum --check SHA256SUMS

contract-check:
	go run ./tools/check-contract
