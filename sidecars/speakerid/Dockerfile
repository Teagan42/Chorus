# syntax=docker/dockerfile:1
# Built from the repo root (the uv workspace root), so the lockfile is the
# one the whole workspace resolved: docker build -f sidecars/speakerid/Dockerfile .
#
# Pinned by digest-equivalent tags for the same reason as the Go tools: a base
# image or uv release must not change what the sidecar embeds.
FROM python:3.13.16-slim-bookworm
COPY --from=ghcr.io/astral-sh/uv:0.11.32 /uv /bin/uv

ENV UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_PROJECT_ENVIRONMENT=/opt/venv \
    UV_NO_DEV=1

WORKDIR /src

# Dependencies first, in their own layer: torch is gigabytes and a change to
# the sidecar's own source must not re-download it.
COPY pyproject.toml uv.lock ./
COPY sidecars/speakerid/pyproject.toml sidecars/speakerid/
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --package speakerid --no-install-workspace

COPY sidecars/speakerid/src sidecars/speakerid/src
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --package speakerid

# The checkpoint is baked in, so a running container never reaches the Hub
# and serves the revision it was built with. "main" until a commit is
# recorded (ADR-0025).
ARG SPEAKERID_MODEL_REVISION=main
ENV SPEAKERID_MODEL_DIR=/models/ecapa \
    SPEAKERID_MODEL_REVISION=$SPEAKERID_MODEL_REVISION \
    SPEAKERID_PORT=8890
RUN /opt/venv/bin/speakerid --download-only

EXPOSE 8890
CMD ["/opt/venv/bin/speakerid"]
