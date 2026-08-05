# syntax=docker/dockerfile:1

# Golang Builder
FROM docker.io/library/debian:trixie-slim AS gobuilder

WORKDIR /go/src/github.com/galexrt/extended-ceph-exporter/
COPY . ./
RUN apt-get update && \
    apt-get install -y curl git make golang \
        libcephfs-dev librbd-dev librados-dev gcc pkg-config && \
    git config --global --add safe.directory /go/src/github.com/galexrt/extended-ceph-exporter
RUN make build

# Final Image
FROM docker.io/library/debian:trixie-slim

ARG BUILD_DATE="N/A"
ARG REVISION="N/A"

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
    org.opencontainers.image.version="N/A"

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
