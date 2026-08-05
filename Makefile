SHELL := /usr/bin/env bash

comma=,

PROJECTNAME ?= extended-ceph-exporter

GO111MODULE  ?= on
GO           ?= go
PREFIX       ?= $(shell pwd)
BIN_DIR      ?= $(PREFIX)/.bin
TARBALL_DIR  ?= $(PREFIX)/.tarball
PACKAGE_DIR  ?= $(PREFIX)/.package
ARCH         ?= amd64
PACKAGE_ARCH ?= linux-amd64

VERSION      := $(shell cat VERSION)
TOPDIR       := $(shell pwd)

# The GOHOSTARM and PROMU parts have been taken from the prometheus/promu repository
# which is licensed under Apache License 2.0 Copyright 2018 The Prometheus Authors
FIRST_GOPATH := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))

GOHOSTOS     ?= $(shell $(GO) env GOHOSTOS)
GOHOSTARCH   ?= $(shell $(GO) env GOHOSTARCH)

ifeq (arm, $(GOHOSTARCH))
	GOHOSTARM ?= $(shell GOARM= $(GO) env GOARM)
	GO_BUILD_PLATFORM ?= $(GOHOSTOS)-$(GOHOSTARCH)v$(GOHOSTARM)
else
	GO_BUILD_PLATFORM ?= $(GOHOSTOS)-$(GOHOSTARCH)
endif

PROMU_VERSION ?= 0.13.0
PROMU_URL     := https://github.com/prometheus/promu/releases/download/v$(PROMU_VERSION)/promu-$(PROMU_VERSION).$(GO_BUILD_PLATFORM).tar.gz

PROMU := $(FIRST_GOPATH)/bin/promu
# END copied code

GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT         := $(FIRST_GOPATH)/bin/golangci-lint

pkgs = $(shell go list ./... | grep -v /vendor/ | grep -v /test/)
GOFILES = $(shell find . -path ./vendor -prune -o -name '*.go' -print)

CONTAINER_IMAGE_NAME ?= ghcr.io/e2enetworks-oss/extended-ceph-exporter
CONTAINER_IMAGE_TAG  ?= $(subst /,-,$(shell git rev-parse --abbrev-ref HEAD))
CONTAINER_ARCHES ?= linux/amd64,linux/arm64

all: format style vet lint test build

build: promu
	@echo ">> building binaries"
	$(PROMU) build -v --prefix $(PREFIX)

check_license:
	@OUTPUT="$$($(PROMU) check licenses)"; \
	if [[ $$OUTPUT ]]; then \
		echo "Found go files without license header:"; \
		echo "$$OUTPUT"; \
		exit 1; \
	else \
		echo "All files with license header"; \
	fi

container:
	$(MAKE) container-build

container-build:
	@echo ">> building container image"
	docker build \
		--build-arg BUILD_DATE="$(shell date -u +'%Y-%m-%dT%H:%M:%SZ')" \
		--build-arg REVISION="$(shell git rev-parse HEAD)" \
		--build-arg VERSION="$(VERSION)" \
		-t "$(CONTAINER_IMAGE_NAME):$(CONTAINER_IMAGE_TAG)" \
		.
	docker tag "$(CONTAINER_IMAGE_NAME):$(CONTAINER_IMAGE_TAG)" "$(CONTAINER_IMAGE_NAME):latest"

container-publish:
	docker push "$(CONTAINER_IMAGE_NAME):$(CONTAINER_IMAGE_TAG)"
	docker push "$(CONTAINER_IMAGE_NAME):latest"

# Extract the release binary out of an already-published multi-architecture
# image and lay it out as the tarball attached to the GitHub Release. `docker
# create` never starts the container, so a foreign-architecture image can be
# unpacked on an amd64 host without QEMU. The release workflow calls this after
# the image manifest is pushed.
release-binaries:
	mkdir -p .output
	cd .output/ && \
	for ARCH in $(subst $(comma), ,$(CONTAINER_ARCHES)); do \
		RELEASE_FILE_NAME="extended-ceph-exporter-$$(echo $(CONTAINER_IMAGE_TAG) | sed -e 's/^v//').$$(echo $$ARCH | sed -e 's/\//-/g')"; \
		mkdir -p "$$RELEASE_FILE_NAME"; \
		cp -vf ../LICENSE "$$RELEASE_FILE_NAME/"; \
		CONTAINER_ID="$$(docker create --platform $$ARCH "$(CONTAINER_IMAGE_NAME):$(CONTAINER_IMAGE_TAG)")"; \
		docker cp "$$CONTAINER_ID:/bin/extended-ceph-exporter" "$$RELEASE_FILE_NAME/"; \
		docker rm "$$CONTAINER_ID"; \
		tar czf "$$RELEASE_FILE_NAME.tar.gz" "$$RELEASE_FILE_NAME"; \
		rm -rf "$$RELEASE_FILE_NAME"; \
	done

format:
	go fmt $(pkgs)

golangci-lint:
	@if ! [ -x "$(GOLANGCI_LINT)" ] || \
		! "$(GOLANGCI_LINT)" --version 2>/dev/null | grep -q "$(patsubst v%,%,$(GOLANGCI_LINT_VERSION))"; then \
		echo ">> installing golangci-lint $(GOLANGCI_LINT_VERSION)"; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
			| sh -s -- -b $(FIRST_GOPATH)/bin $(GOLANGCI_LINT_VERSION); \
	fi

lint: golangci-lint
	@echo ">> linting code"
	@$(GOLANGCI_LINT) run --timeout 10m

promu:
	$(eval PROMU_TMP := $(shell mktemp -d))
	curl -s -L $(PROMU_URL) | tar -xvzf - -C $(PROMU_TMP)
	mkdir -p $(FIRST_GOPATH)/bin
	cp $(PROMU_TMP)/promu-$(PROMU_VERSION).$(GO_BUILD_PLATFORM)/promu $(FIRST_GOPATH)/bin/promu
	rm -r $(PROMU_TMP)

style:
	@echo ">> checking code style"
	@OUTPUT="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$OUTPUT" ]; then \
		echo "The following files are not gofmt formatted:"; \
		echo "$$OUTPUT"; \
		echo "Run 'make format' to fix them."; \
		exit 1; \
	else \
		echo "All files gofmt formatted"; \
	fi

tarball: tree                                                                                                                                       
	@echo ">> building release tarball"
	@$(PROMU) tarball --prefix $(TARBALL_DIR) $(BIN_DIR)

clean:
	rm -rf $(PROJECTNAME) $(PROJECTNAME).spec $(PROJECTNAME)-$(VERSION).tar.gz 
	
test:
	@echo ">> running tests"
	@$(GO) test -race -count=1 $(pkgs)

test-short:
	@echo ">> running short tests"
	@$(GO) test -short $(pkgs)

vet:
	@echo ">> vetting code"
	@$(GO) vet $(pkgs)

# ── Release versioning ──────────────────────────────────────────────────────
# The VERSION file is the single source of truth. A release is cut by tagging
# v$(VERSION) on main; check-version is what stops a tag being cut against a
# changelog that never caught up. See RELEASE.md.

check-version:
	@set -e; \
	V="$(VERSION)"; \
	FAIL=0; \
	if ! echo "$$V" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$'; then \
		echo "INVALID  VERSION is not semantic versioning: $$V"; FAIL=1; \
	fi; \
	if ! grep -Eq "^## $$V / [0-9]{4}-[0-9]{2}-[0-9]{2}$$" CHANGELOG.md; then \
		echo "MISSING  CHANGELOG.md has no '## $$V / YYYY-MM-DD' section"; \
		echo "         rename the '## Unreleased' heading when cutting the release"; FAIL=1; \
	fi; \
	if [ "$$FAIL" = "0" ]; then \
		echo "VERSION $$V agrees with CHANGELOG.md"; \
	else \
		echo "Add the CHANGELOG.md entry for $$V, then re-run."; exit 1; \
	fi

release-notes:
	@./scripts/extract-changelog.sh "$(VERSION)"

.PHONY: all build check_license check-version container container-build container-publish \
	format golangci-lint lint promu release-binaries release-notes style \
	tarball test test-short vet clean
