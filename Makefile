# Set the shell to bash always
SHELL := /bin/bash

# Every file `make generate` writes.
GENERATED := ':(glob)apis/**/zz_generated.*.go' helm/incident-controller-crds/templates

.PHONY: help
help: ## print this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: generate
generate: ## generate deepcopy methodsets and the Incident CRD (into helm/incident-controller-crds)
	cd apis && go generate -tags generate ./...

.PHONY: check-generate
check-generate: generate ## fail if the committed generated files differ from a fresh generate
	@git diff --exit-code -- $(GENERATED) || { echo "generated files are stale: run 'make generate' and commit"; exit 1; }
	@untracked="$$(git ls-files --others --exclude-standard -- $(GENERATED))"; \
	  if [ -n "$$untracked" ]; then echo "untracked generated files: $$untracked"; exit 1; fi

.PHONY: build
build: ## build all packages
	go build ./...

.PHONY: test
test: ## run unit tests
	go test -race -cover ./...

.PHONY: lint
lint: ## run go vet
	go vet ./...

.PHONY: check
check: build lint test check-generate ## everything CI runs
