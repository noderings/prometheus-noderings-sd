VERSION  ?= $(or $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null)),dev)
REVISION ?= $(or $(shell git rev-parse --verify -q HEAD 2>/dev/null),unknown)
BRANCH   ?= $(or $(shell git symbolic-ref --short -q HEAD 2>/dev/null),unknown)
IMAGE    ?= ghcr.io/noderings/prometheus-noderings-sd
PKG      := github.com/prometheus/common/version
LDFLAGS  := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Revision=$(REVISION) \
            -X $(PKG).Branch=$(BRANCH) -X $(PKG).BuildUser=$(or $(USER),unknown)@$(shell hostname) \
            -X $(PKG).BuildDate=$(shell date -u +%Y%m%d-%H:%M:%S)

.PHONY: build test lint image e2e

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/prometheus-noderings-sd ./cmd/prometheus-noderings-sd

test:
	go test -race ./...

lint:
	golangci-lint run

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) -t $(IMAGE):$(VERSION) .

# Runs the binary and a real Prometheus against the live API. Needs NODERINGS_TOKEN.
e2e: build
	hack/e2e.sh
