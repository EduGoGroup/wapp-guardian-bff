# Makefile — wapp-guardian-bff
#
# Régimen CI/CD nuevo (decisión del dueño, 2026-08-01): este repo NO tiene
# workflows de GitHub Actions — el BFF no corta releases, así que su red es
# íntegramente LOCAL. Valida aquí antes de mergear y pushear.
#   - ci-local  agrega los gates que exige el grupo (fmt, vet, lint, test, build).
#   - ci-docker reproduce el toolchain fijado (imagen golang + golangci-lint).
#
# El módulo es público (github.com/EduGoGroup/wapp-guardian-bff): ci-docker no
# necesita GOPRIVATE ni .netrc.

GO_VERSION   := 1.26.5
LINT_VERSION := v2.12.2
GO           := GOWORK=off go

.PHONY: fmt-check vet lint build test ci-local ci-docker

fmt-check: ## gofmt -l vacío (sin archivos sin formatear)
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Archivos sin gofmt:"; echo "$$unformatted"; exit 1; \
	fi

vet: ## go vet ./...
	$(GO) vet ./...

lint: ## golangci-lint $(LINT_VERSION) (binario fijado — no el de ~/go/bin)
	GOWORK=off golangci-lint run --timeout=5m

build: ## go build ./...
	$(GO) build ./...

test: ## Tests unitarios con -race
	$(GO) test -race ./...

ci-local: fmt-check vet lint test build ## Pre-push: fmt + vet + lint + test + build

ci-docker: ## Simula el CI en Docker (Go $(GO_VERSION) + golangci-lint $(LINT_VERSION)) — requiere Docker
	@docker run --rm \
		-e GOFLAGS=-buildvcs=false \
		-v "$$(go env GOPATH)/pkg/mod:/go/pkg/mod" \
		-v "$(CURDIR):/workspace" -w /workspace \
		golang:$(GO_VERSION)-bookworm \
		bash -c "set -e; curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /usr/local/bin $(LINT_VERSION) && make ci-local"
