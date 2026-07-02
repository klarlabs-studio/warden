# Warden — local CI. `make ci` mirrors the GitHub pipeline order:
# quality (fmt, vet, lint, gocritic) -> security (nox) -> tests -> e2e.

.PHONY: ci fmt fmt-check vet lint gocritic sec vuln test cover e2e build install clean

ci: fmt-check vet lint gocritic sec vuln test cover e2e ## Run the full pipeline in order

fmt: ## Format the tree
	gofmt -w .

fmt-check: ## Fail if anything is not gofmt-clean
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (govet, staticcheck, gocritic, misspell, …)
	golangci-lint run ./...

gocritic: ## Standalone gocritic (diagnostic/style/performance)
	gocritic check ./...

sec: ## nox security scan (findings baselined in .nox/baseline.json)
	nox scan .

vuln: ## govulncheck — reachable Go stdlib + dependency vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

test: ## Unit tests with race + coverage
	go test ./... -race -cover

cover: ## Coverage policy check (coverctl)
	coverctl check

e2e: ## End-to-end tests (builds the binary, drives real git)
	go test -tags e2e ./e2e/ -v

build: ## Build the binary
	go build -o warden .

install: ## Install to GOPATH/bin (needed for the hook shims to find warden)
	go install .

clean:
	rm -f warden findings.json results.sarif
