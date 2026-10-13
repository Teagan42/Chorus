# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f cmd/enroll/Dockerfile .
FROM golang:1.27.0-trixie AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ cmd/
COPY internal/ internal/

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/enroll ./cmd/enroll

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/enroll /enroll
ENTRYPOINT ["/enroll"]
