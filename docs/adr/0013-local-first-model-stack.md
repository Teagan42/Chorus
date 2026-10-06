# 0013. Run models locally behind capability-declaring providers

- **Status:** accepted
- **Source:** SPEC §10

Local-first, with a cloud escape hatch per provider. Every model sits behind a
streaming gRPC/HTTP contract and declares its capabilities — streaming? tool
calls? interruption? — so a build-time check rejects a configured pipeline that
cannot satisfy the architecture (CONTRIBUTING §2).

| Role | Choice | Why this one |
|---|---|---|
| STT | Parakeet TDT 0.6B v2 | Streaming, faster than Whisper large at better accuracy. English-only; faster-whisper if multilingual is needed. |
| LLM | Qwen3 32B / 30B-A3B on vLLM | Template emits `content` and `tool_calls` together, and vLLM streams tool-call parsing. Both are hard requirements of §4.1. |
| TTS | Kokoro-82M | Sentence-level streaming, sub-200 ms first chunk. Orpheus for expression, Piper as degraded fallback. |
| Speaker ID | ECAPA-TDNN / TitaNet-L | Embeddings plus cosine against enrolled centroids. |
| Endpointing | Smart Turn v2 | Purpose-built; avoids prompting the big model. |

## Alternatives rejected

Ollama and llama.cpp for the LLM seam. Both are weaker at exactly the thing
§4.1 depends on: streaming incremental tool-call parsing.

## Consequences

The standing risk is local tool-calling quality, which is why the provider
abstraction exists — a frontier model drops into the LLM seam without touching
the orchestrator. A degraded local profile covers "lights off" when everything
else is down, but local-model limits do not get to dictate the architecture.
