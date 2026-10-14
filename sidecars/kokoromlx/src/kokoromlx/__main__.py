"""Entry point: fetch and load the weights, warm the GPU, then serve. The port
opens only once a render has succeeded, so anything that can reach it can be
answered."""

import argparse
import os

import uvicorn

from kokoromlx.app import create_app
from kokoromlx.voice import MlxVoice


def main() -> None:
    p = argparse.ArgumentParser(prog="kokoromlx")
    # Loopback, unlike the containerised sidecars: this one only ever runs on
    # somebody's Mac, where 0.0.0.0 would put the household's voice on the LAN.
    # Compose reaches the host by gateway, so chorusd in a container needs
    # --host 0.0.0.0 here.
    p.add_argument("--host", default=os.environ.get("KOKOROMLX_HOST", "127.0.0.1"))
    # Kokoro-FastAPI's port, so KOKORO_URL needs no change -- and so the two
    # cannot run at once, which is the point.
    p.add_argument("--port", type=int, default=int(os.environ.get("KOKOROMLX_PORT", "8880")))
    # Relative to the repo root, where `task tts:serve` runs it.
    p.add_argument(
        "--model-dir", default=os.environ.get("KOKOROMLX_MODEL_DIR", ".models/kokoromlx")
    )
    p.add_argument(
        "--download-only",
        action="store_true",
        help="fetch the pinned weights into --model-dir and exit",
    )
    args = p.parse_args()

    voice = MlxVoice(args.model_dir)
    voice.load(download_only=args.download_only)
    if args.download_only:
        return
    uvicorn.run(create_app(voice), host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":
    main()
