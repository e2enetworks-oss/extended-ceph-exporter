# syntax=docker/dockerfile:1

# Golang Builder
#
# Base images are pulled through mirror.gcr.io, Google's pull-through cache for
# Docker Hub, so builds are not subject to Docker Hub's anonymous pull rate limit.
#
# The Go minor version tracks the `go` directive in go.mod. Pinning it here beats
# Debian's `golang` package, which trails go.mod and forces the build to download
# a second toolchain at compile time.
FROM mirror.gcr.io/library/golang:1.26-trixie AS gobuilder

WORKDIR /go/src/github.com/galexrt/extended-ceph-exporter/

# Dependencies before sources: this layer is keyed only on the package list, so
# editing a collector no longer re-runs apt on both architecture runners.
RUN apt-get update && \
    apt-get install -y --no-install-recommends make \
        libcephfs-dev librbd-dev librados-dev pkg-config && \
    rm -rf /var/lib/apt/lists/*

COPY . ./
# promu reads the revision and branch out of .git for the version ldflags, so the
# build needs git to trust a directory it does not own.
RUN git config --global --add safe.directory /go/src/github.com/galexrt/extended-ceph-exporter && \
    make build

# Final Image
FROM mirror.gcr.io/library/debian:trixie-slim

ARG BUILD_DATE="N/A"
ARG REVISION="N/A"
ARG VERSION="N/A"

# image.source is what GHCR uses to link a published package to a repository and
# inherit its visibility and permissions, so it has to name this fork rather than
# upstream. Code attribution lives in LICENSE and the per-file copyright headers.
LABEL org.opencontainers.image.authors="E2E Networks" \
    org.opencontainers.image.created="${BUILD_DATE}" \
    org.opencontainers.image.title="e2enetworks-oss/extended-ceph-exporter" \
    org.opencontainers.image.description="A Prometheus exporter for \"extended\" Ceph metrics, including per-RBD-image tenant ownership, QoS limits and capacity. Fork of galexrt/extended-ceph-exporter." \
    org.opencontainers.image.documentation="https://github.com/e2enetworks-oss/extended-ceph-exporter/blob/main/README.md" \
    org.opencontainers.image.url="https://github.com/e2enetworks-oss/extended-ceph-exporter" \
    org.opencontainers.image.source="https://github.com/e2enetworks-oss/extended-ceph-exporter" \
    org.opencontainers.image.revision="${REVISION}" \
    org.opencontainers.image.vendor="E2E Networks" \
    org.opencontainers.image.version="${VERSION}"

VOLUME /config
VOLUME /realms

RUN apt-get update && \
    apt-get install -y libcephfs-dev librbd-dev librados-dev \
        ca-certificates

COPY --from=gobuilder /go/src/github.com/galexrt/extended-ceph-exporter/extended-ceph-exporter /bin/extended-ceph-exporter
# Copy default configs
COPY --from=gobuilder /go/src/github.com/galexrt/extended-ceph-exporter/config.example.yaml /config/config.yaml
COPY --from=gobuilder /go/src/github.com/galexrt/extended-ceph-exporter/realms.example.yaml /realms/realms.yaml

ENTRYPOINT ["/bin/extended-ceph-exporter"]
