# syntax=docker/dockerfile:1
# Built from the repo root (the uv workspace root), so the lockfile is the
# one the whole workspace resolved: docker build -f sidecars/smartturn/Dockerfile .
#
# Pinned like sidecars/speakerid: a base image or uv release must not change
# when a satellite decides someone has finished talking.
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
COPY sidecars/smartturn/pyproject.toml sidecars/smartturn/
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --package smartturn --no-install-workspace

COPY sidecars/smartturn/src sidecars/smartturn/src
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --package smartturn

# The model is baked in: read out of the pinned wheel here and verified
# against both digests in model.py, so a running container never reaches the
# network and serves exactly the bytes it was built with (ADR-0036).
ENV SMARTTURN_MODEL_DIR=/models/smartturn \
    SMARTTURN_PORT=8891
RUN /opt/venv/bin/smartturn --download-only

EXPOSE 8891
CMD ["/opt/venv/bin/smartturn"]
