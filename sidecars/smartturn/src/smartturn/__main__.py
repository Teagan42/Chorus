"""Entry point: fetch and load the model, then serve. The port opens only once
the model is ready, so a compose healthcheck on it is a readiness check."""

import argparse
import os

import uvicorn

from smartturn.app import create_app
from smartturn.model import OnnxTurn


def main() -> None:
    p = argparse.ArgumentParser(prog="smartturn")
    p.add_argument("--host", default=os.environ.get("SMARTTURN_HOST", "0.0.0.0"))
    p.add_argument("--port", type=int, default=int(os.environ.get("SMARTTURN_PORT", "8891")))
    p.add_argument(
        "--model-dir", default=os.environ.get("SMARTTURN_MODEL_DIR", "/models/smartturn")
    )
    p.add_argument(
        "--threads",
        type=int,
        default=int(os.environ.get("SMARTTURN_THREADS", "0")) or None,
        help="runtime threads; defaults to the CPU count, capped",
    )
    p.add_argument(
        "--download-only",
        action="store_true",
        help="fetch the pinned model into --model-dir, verify it, and exit; used at image build",
    )
    args = p.parse_args()

    turn = OnnxTurn(args.model_dir, threads=args.threads)
    turn.load(download_only=args.download_only)
    if args.download_only:
        return
    uvicorn.run(create_app(turn), host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":
    main()
