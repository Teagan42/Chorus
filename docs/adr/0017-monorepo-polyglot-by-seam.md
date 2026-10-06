# 0017. One repo, polyglot by seam

- **Status:** accepted
- **Source:** SPEC §2, §13 · commits `248ccd7`, `1a7420b`

Go where the problem is a concurrent socket server, Python where the model
ecosystem is Python, CUE where the problem is cross-field policy. No
shoehorning in either direction. The device component, orchestrator, and
sidecars share a protocol contract that must version together, so they live in
one repo — split repos mean a coordinated release dance for a one-person
project. Deployment is docker-compose with the model sidecars pinned to GPU.

Satellite inventory splits by volatility: mDNS (`_esphomelib._tcp`) discovers
addresses, while static YAML carries Noise PSKs, room assignment, and
capability profile. Secrets stay out of a database that would then need its own
backup.

## Consequences

PSKs never enter the repo. `devices.yaml` is gitignored and landed first so no
later commit can stage it by accident (`1a7420b`); `.githooks/pre-commit`
blocks it; `devices.example.yaml` ships a placeholder that `config.Load`
rejects (`8de26ea`, `8212e3b`). A committed PSK cannot be un-leaked.

`config.Load` also validates that a PSK decodes to 32 bytes, because otherwise
a typo surfaces as an opaque Noise handshake failure against real hardware
(`248ccd7`).

The persona lives in the system prompt as a versioned artifact. Prompt versions
are recorded per event, so persona changes are A/B-testable against replayed
traces.
