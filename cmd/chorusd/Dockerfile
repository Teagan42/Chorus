# syntax=docker/dockerfile:1
# Built from the repo root, so the module is the one `task check` tested:
# docker build -f cmd/chorusd/Dockerfile .
#
# Pinned for the same reason as the Go tools: a base image release must not
# change what the daemon is built with or runs on. The Go tag is the version
# go.mod names; the runtime image is pinned by digest because its tag moves.
FROM golang:1.27.0-trixie AS build

WORKDIR /src

# Dependencies first, in their own layer: a change to the daemon's own source
# must not re-download the module graph.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ cmd/
COPY internal/ internal/

# CGO_ENABLED=0: a static binary, and nothing here needs libc. -trimpath and
# -s -w for the same reason as the release binaries: a reproducible, small
# artifact with the version it was built from.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/chorusd ./cmd/chorusd

# The blob directory exists in the image, owned by the runtime user, so a
# named volume first mounted there copies that ownership and is writable
# without a root entrypoint.
RUN mkdir -p /out/blobs

# Static, nonroot, with CA certificates for an https model endpoint and
# nothing else: no shell to drop into on a box that hears the household.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/chorusd /chorusd
COPY --from=build --chown=nonroot:nonroot /out/blobs /var/lib/chorus/blobs

ENV CHORUS_LISTEN=:6055 \
    CHORUS_BLOB_DIR=/var/lib/chorus/blobs

EXPOSE 6055
ENTRYPOINT ["/chorusd"]
CMD ["-devices", "/etc/chorus/devices.yaml"]
