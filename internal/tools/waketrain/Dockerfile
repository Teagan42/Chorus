# syntax=docker/dockerfile:1
# The wake-word trainer (ADR-0078): microWakeWord at a pinned commit, and
# this directory's pipeline over it. Built from this directory:
#
#   docker build -t chorus-waketrain internal/tools/waketrain
#
# `task wake:train` builds it and runs it with the household's .wake mounted
# at /wake. A GPU is optional: `--gpus all` on a host with the NVIDIA
# container toolkit trains on it; without one TensorFlow uses the CPU,
# which is slow (hours for the default 10,000 steps, not minutes).
#
# Pinned like the sidecars, by digest here as no lockfile pins a base: a
# base image or uv release must not change what a model is trained with.
FROM python:3.11.12-slim-bookworm@sha256:dbf1de478a55d6763afaa39c2f3d7b54b25230614980276de5cacdde79529d0c
COPY --from=ghcr.io/astral-sh/uv:0.11.32 /uv /bin/uv

# git fetches microWakeWord and the audio-metadata fork by commit;
# g++ for any dependency without a wheel. Debian's versions are not pinned:
# bookworm takes only fixes, and git and g++ shape no model.
# hadolint ignore=DL3008
RUN apt-get update \
 && apt-get install -y --no-install-recommends git g++ \
 && rm -rf /var/lib/apt/lists/*

ENV UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /opt/waketrain

# Dependencies first, in their own layer: a change to the pipeline must not
# reinstall TensorFlow.
COPY requirements.txt ./
RUN uv pip sync --system --no-cache requirements.txt

# microWakeWord itself, at the commit training.yaml starts from, on the path
# rather than installed: a non-editable install drops its audio and layers
# packages, which have no __init__.py (requirements.in).
ARG MICROWAKEWORD_COMMIT=4665173cd35f1cff9a61e06fc427f124766c488e
RUN git clone --filter=blob:none https://github.com/kahrendt/microWakeWord /opt/microWakeWord \
 && git -C /opt/microWakeWord checkout --quiet "$MICROWAKEWORD_COMMIT"
ENV PYTHONPATH=/opt/microWakeWord

COPY wake.py layout.py manifest.py detect.py trainconfig.py features.py evaluate.py training.yaml ./

ENTRYPOINT ["python", "/opt/waketrain/wake.py"]
