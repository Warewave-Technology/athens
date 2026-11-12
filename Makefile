SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE   ?= ghcr.io/warewave-technology/athens:$(VERSION)
PKG     := github.com/Warewave-Technology/athens/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildDate=$(DATE)
GOFLAGS := -trimpath
export CGO_ENABLED=0

.PHONY: all build web go dev test test-int e2e lint image clean

all: build

web/node_modules: web/package.json web/pnpm-lock.yaml
	cd web && pnpm install --frozen-lockfile
	@touch web/node_modules

web: web/node_modules
	cd web && pnpm build

go:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/athens ./cmd/athens

build: web go

# Backend on :7080 proxies the SPA to Vite on :5173. Open http://localhost:7080.
dev: web/node_modules
	@trap 'kill 0' EXIT; \
	(cd web && pnpm dev) & \
	go run ./hack/mockidp & \
	cfg=hack/dev/config.yaml; [ -f hack/dev/config.local.yaml ] && cfg=hack/dev/config.local.yaml; \
	go run ./cmd/athens --dev --kubeconfig $${KUBECONFIG:-hack/kind/.kube/config} --config $${ATHENS_CONFIG:-$$cfg}; \
	wait

test:
	go test -race -count=1 ./...
	cd web && pnpm test

test-int:
	go test -race -count=1 -tags integration -timeout 20m ./...

e2e:
	./hack/kind/e2e.sh

lint: web/node_modules
	golangci-lint run ./...
	cd web && pnpm lint && pnpm typecheck

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t $(IMAGE) .

clean:
	rm -rf bin web/dist/assets web/dist/index.html web/dist/favicon.svg
