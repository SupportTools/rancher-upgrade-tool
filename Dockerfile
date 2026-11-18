# Fetch tiers (cluster-services-1436; no direct Docker Hub / dl-cdn / proxy.golang.org fetch on this path):
#   - base images: the Harbor `dockerhub` PROXY cache, pinned by index digest.
#   - apk: the org fetch-cache /alpine/ (a cache we run, in-cluster name) through build/apk-add.sh (vendored
#     from SupportTools/harbor-scanner-grype scripts/apk-add.sh @ bce5cbd2). Its fallback to dl-cdn is logged
#     ("apk source: MIRROR FAILED"); CI passes APK_FALLBACK=none so a mirror outage fails the build instead.
#     apk verifies every index and package signature against the base image's keys either way.
#   - Go modules: Athens (GOPROXY build-arg, default https://goproxy.support.tools, a cache we run); go.sum
#     and the checksum DB verify them.

# Build Stage
FROM harbor.support.tools/dockerhub/library/golang:1.22.4-alpine3.20@sha256:ace6cc3fe58d0c7b12303c57afe6d6724851152df55e08057b43990b927ad5e8 AS builder
ARG GOPROXY=https://goproxy.support.tools

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go.mod and go.sum files to the workspace
COPY go.mod go.sum ./

# Download all dependencies (-x logs every "# get <url>": the build log shows the proxy that served each module)
RUN echo "go module source: GOPROXY=${GOPROXY}" && go mod download -x

# Copy the source code and required directories to the Working Directory inside the container
COPY . .

# Build identity, passed by CI as --build-arg VERSION="v<run_number>".
#
# These three args were ALREADY being passed by the pipeline and silently
# discarded, because the Dockerfile declared no ARG to receive them. VERSION is
# now linked into the binary and served at /version, which is what lets a deploy
# be verified from outside the cluster -- see the note on `version` in main.go.
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

# Build the Go app
# CGO_ENABLED=0 so the binary is static and does not depend on the runtime
# image's musl. -trimpath strips local filesystem paths, which otherwise make
# the binary differ between machines for identical source.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X main.buildVersion=${VERSION}" -o main .

# Final Stage
# Pinned to match the builder's alpine3.20. Previously `alpine:latest`, which
# combined with the nightly cron meant production ran a different base image
# most mornings for identical source.
#
# Now pinned by index digest through the Harbor dockerhub proxy (cluster-services-1436), so the base
# no longer moves across 3.20.x patch releases either.
FROM harbor.support.tools/dockerhub/library/alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
ARG APK_MIRROR=http://fetch-cache.fetch-cache.svc.a1-ops-prd.local/alpine
ARG APK_FALLBACK=upstream

# USN-8944-1: upgrade ca-certificates and ca-certificates-bundle so the leaked
# eMudhra emSign roots (ECC G3, ECC C3, C1) are dropped from the trust bundle.
# CA trust (cluster-services-1392, fleet CA standard 2026-11-13): no eMudhra emSign root at all. Alpine's
# 20261020 bundle still ships emSign Root CA - G1, so build/noemsign-ca.sh (canonical copy: cluster-services
# noemsign/noemsign-ca.sh) strips every emSign certificate by decoded subject and its guard fails the build if
# one is left. openssl is a temporary virtual package; the trust store is checksummed across its removal.
RUN --mount=type=bind,source=build/noemsign-ca.sh,target=/tmp/noemsign-ca.sh \
    --mount=type=bind,source=build/apk-add.sh,target=/tmp/apk-add.sh \
    APK_MIRROR="$APK_MIRROR" APK_FALLBACK="$APK_FALLBACK" sh /tmp/apk-add.sh --upgrade ca-certificates ca-certificates-bundle \
 && APK_MIRROR="$APK_MIRROR" APK_FALLBACK="$APK_FALLBACK" sh /tmp/apk-add.sh --virtual .noemsign openssl \
 && sh /tmp/noemsign-ca.sh strip / \
 && sh /tmp/noemsign-ca.sh guard / \
 && sum0="$(tar -c /etc/ssl /usr/share/ca-certificates 2>/dev/null | sha256sum)" \
 && apk --no-network del .noemsign \
 && [ "$sum0" = "$(tar -c /etc/ssl /usr/share/ca-certificates 2>/dev/null | sha256sum)" ]

WORKDIR /app

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Copy the static files and data directory
COPY --from=builder /app/static ./static
# Copy ONLY the live catalog, not the whole data directory.
#
# data/upgrade-paths.json is the superseded pre-rewrite dataset. It stays in the
# repo because 63 of its 70 entries are NOT re-derivable from upstream (endoflife
# publishes only the latest patch per cycle, and SUSE retires old per-version
# matrix pages), but it has no business in the image: dead weight, and a second
# compatibility dataset sitting next to the live one invites someone to load the
# wrong file.
COPY --from=builder /app/data/catalog.json ./data/catalog.json

# Expose port 3000 to the outside world
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
