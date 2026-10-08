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

# Dependencies first, in their own layer: a change to the sidecar's own
# source must not reinstall the runtime.
COPY pyproject.toml uv.lock ./
COPY sidecars/speakerid/pyproject.toml sidecars/speakerid/
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --package speakerid --no-install-workspace

COPY sidecars/speakerid/src sidecars/speakerid/src
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --package speakerid

# The model is baked in: one release asset, fetched here and verified against
# the digest pinned in embedder.py, so a running container never reaches the
# network and serves exactly the bytes it was built with (ADR-0029).
ENV SPEAKERID_MODEL_DIR=/models/speakerid \
    SPEAKERID_PORT=8890
RUN /opt/venv/bin/speakerid --download-only

EXPOSE 8890
CMD ["/opt/venv/bin/speakerid"]
