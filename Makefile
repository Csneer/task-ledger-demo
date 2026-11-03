.PHONY: check fmt-check protocol-check contract-check skill-smoke release release-smoke browser-check

# Runtime checks plus isolated HTTP verification of the high-level Skill client.
check: fmt-check protocol-check contract-check
	go test -race ./...
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s skills/task-ledger/scripts -p 'test_*.py'
	$(MAKE) skill-smoke
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools -p 'test_*.py'
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/public-demo -p 'test_*.py'
	node --check deploy/public-demo/bootstrap.js
	go vet ./...
	go build ./...
	node --test web/*.test.mjs

fmt-check:
	test -z "$$(gofmt -l cmd internal tools web contract/v1/embed.go protocol/upstream/embed.go)"

protocol-check:
	cd protocol/upstream && sha256sum --check SHA256SUMS

contract-check:
	go run ./tools/check-contract

skill-smoke:
	@set -eu; \
	ledger_smoke_dir="$$(mktemp -d -t task-ledger-skill-check-XXXXXX)"; \
	go build -o "$$ledger_smoke_dir/taskledgerd" ./cmd/taskledgerd; \
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/easy_skill_smoke.py \
	  --daemon "$$ledger_smoke_dir/taskledgerd" --dir "$$ledger_smoke_dir/run"

release:
	bash tools/build-release.sh

release-smoke:
	bash tools/smoke-release.sh

browser-check:
	bash tools/verify-browser.sh
