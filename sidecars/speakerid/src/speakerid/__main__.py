"""Entry point: load the checkpoint, then serve. The port opens only once the
model is ready, so a compose healthcheck on it is a readiness check."""

import argparse
import os

import uvicorn

from speakerid.app import create_app
from speakerid.embedder import DEFAULT_REVISION, EcapaEmbedder


def main() -> None:
    p = argparse.ArgumentParser(prog="speakerid")
    p.add_argument("--host", default=os.environ.get("SPEAKERID_HOST", "0.0.0.0"))
    p.add_argument("--port", type=int, default=int(os.environ.get("SPEAKERID_PORT", "8890")))
    p.add_argument("--model-dir", default=os.environ.get("SPEAKERID_MODEL_DIR", "/models/ecapa"))
    p.add_argument(
        "--revision", default=os.environ.get("SPEAKERID_MODEL_REVISION", DEFAULT_REVISION)
    )
    p.add_argument("--device", default=os.environ.get("SPEAKERID_DEVICE") or None)
    p.add_argument(
        "--download-only",
        action="store_true",
        help="fetch the checkpoint into --model-dir and exit; used at image build",
    )
    args = p.parse_args()

    embedder = EcapaEmbedder(args.model_dir, revision=args.revision, device=args.device)
    embedder.load(download_only=args.download_only)
    if args.download_only:
        return
    uvicorn.run(create_app(embedder), host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":
    main()
