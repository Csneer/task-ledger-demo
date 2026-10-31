.PHONY: check fmt-check protocol-check contract-check

# Runtime checks cover the trusted Go API, not HTTP or independent clients.
check: fmt-check protocol-check contract-check
	go test -race ./...
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s skills/task-ledger/scripts -p 'test_*.py'
	go vet ./...
	go build ./...

fmt-check:
	test -z "$$(gofmt -l cmd internal tools contract/v1/embed.go protocol/upstream/embed.go)"

protocol-check:
	cd protocol/upstream && sha256sum --check SHA256SUMS

contract-check:
	go run ./tools/check-contract
