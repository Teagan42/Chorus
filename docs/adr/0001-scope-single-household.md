# 0001. Target one household, not a product

- **Status:** accepted
- **Source:** SPEC §1

Chorus serves 2–10 satellites and 1–2 concurrent conversations on a trusted
single-household network. Scope is fixed this narrowly because every later
decision — a shared Postgres journal, PSKs in a static YAML file, no
authorization model between people — is only safe at this scale.

## Forecloses

Multi-tenancy. The journal is partitioned by conversation, not by tenant, and
memory visibility is a per-fact `shareable` flag (§5) rather than an access
control model. Both would need redesign for untrusted users.
