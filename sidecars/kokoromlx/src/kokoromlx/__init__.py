"""Speech synthesis over HTTP for Chorus (SPEC §10), on this Mac's GPU.

The contract is the part of Kokoro-FastAPI's /v1/audio/speech that
internal/provider/kokoro speaks, stated in README.md: a clause of text in, raw
24 kHz samples out, from the same Kokoro-82M weights on MLX (ADR-0092).
"""
