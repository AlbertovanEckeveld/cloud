# Developer entry points. Run `make help` for an overview.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Loads .env (if present; CI sets the environment itself) into a recipe line.
ENV := set -a && { [ ! -f .env ] || . ./.env; } && set +a

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

web/node_modules: web/package-lock.json
	cd web && npm ci --ignore-scripts
	@touch $@

.PHONY: web
web: web/node_modules ## Build the frontend (required before any Go build)
	cd web && npm run build

desktop/frontend/node_modules: desktop/frontend/package-lock.json
	cd desktop/frontend && npm ci --ignore-scripts
	@touch $@

.PHONY: desktop-web
desktop-web: desktop/frontend/node_modules ## Build the desktop app's window (required before its Go build)
	cd desktop/frontend && npm run build

# Wails builds without its CLI: these tags select the desktop runtime. On
# Linux webkit2_41 picks WebKitGTK 4.1; on macOS leave it out.
DESKTOP_TAGS ?= desktop,production,webkit2_41

.PHONY: desktop
desktop: desktop-web ## Build the desktop app into bin/cloud-desktop
	cd desktop && CGO_ENABLED=1 go build -tags $(DESKTOP_TAGS) -trimpath -o ../bin/cloud-desktop .

.PHONY: build
build: web ## Build the platform binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/platform ./cmd/platform

.PHONY: check
check: web desktop-web ## Static checks: gofmt, go vet, svelte-check
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	cd desktop && go vet -tags $(DESKTOP_TAGS) ./...
	cd desktop/frontend && npm run check
	cd web && npm run check

.PHONY: test
test: web desktop-web ## Unit tests (Go with race detector, frontend)
	go test -race -count=1 ./...
	cd desktop && go test -race -count=1 ./...
	cd web && npm test
	sh deploy/update_test.sh

.PHONY: test-integration
test-integration: web ## Integration tests against TEST_DATABASE_URL and the S3 from .env
	@$(ENV) && test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL is not set in .env"; exit 1)
	$(ENV) && DATABASE_URL="$$TEST_DATABASE_URL" go test -tags integration -race -count=1 ./...

.PHONY: run
run: build ## Run the platform with .env
	$(ENV) && ./bin/platform

.PHONY: dev
dev: web/node_modules ## Backend on :8080 plus Vite with hot reload on :5173
	@$(MAKE) -j2 dev-api dev-web

.PHONY: dev-api dev-web
dev-api: web
	$(ENV) && go run ./cmd/platform
dev-web:
	cd web && npm run dev

.PHONY: docker
docker: ## Build the container image platform:$(VERSION)
	docker build --build-arg VERSION=$(VERSION) -t platform:$(VERSION) .

.PHONY: clean
clean: ## Remove build output
	rm -rf bin web/build web/.svelte-kit
