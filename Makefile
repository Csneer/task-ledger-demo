.PHONY: check fmt-check protocol-check

# These checks validate the skeleton, not task behavior or service reliability.
check: fmt-check protocol-check
	go vet ./...
	go build ./...

fmt-check:
	test -z "$$(gofmt -l internal)"

protocol-check:
	cd protocol/upstream && sha256sum --check SHA256SUMS
