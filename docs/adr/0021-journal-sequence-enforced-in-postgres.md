# 0021. Hash-partition the journal and enforce its sequence in Postgres

- **Status:** accepted
- **Source:** SPEC §8, §13 · commit `f30c706`

SPEC §8 stores the journal as Postgres `JSONB` partitioned by conversation.
Partitioning is **HASH on `conversation_id`**, not LIST: conversation ids are
opaque and unbounded, so LIST would need DDL for every new conversation, while
HASH keeps one conversation whole inside one partition with no runtime DDL.
That matches the only read pattern there is — one conversation's whole log — so
every read prunes to a single partition.

The gapless monotonic sequence is enforced **in the database**, because two
writers on one conversation cannot be ordered in Go and `MemStore` deriving the
next sequence from `len(log)` has no equivalent under concurrency. Two
mechanisms cover two different failures: `INSERT ... WHERE seq = max(seq)+1`
rejects a gap, and `PRIMARY KEY (conversation_id, seq)` rejects a writer that
raced past that check. The loser retries at the new `LastSeq`.

```go
import "github.com/teagan42/chorus/internal/journal"

// Both backends satisfy one contract, so replay does not care which holds
// the log. The conformance suite runs against exactly this list.
func stores() []journal.Store {
	return []journal.Store{journal.NewMemStore(), journal.NewPgStore(nil)}
}
```

The fields the reducer and the review UI filter on are real columns —
`conversation_id`, `seq`, `kind`, `actor`, `wall_clock`, `speculative`,
`audio_ref`, and the three version columns. The versions are columns rather
than JSONB specifically because §13 makes a persona change A/B-testable by
replaying the traces produced under one prompt version, which is a `WHERE`
clause. Only the taxonomy-dependent payload is `JSONB`.

One conformance suite runs against both `MemStore` and `PgStore`, which is what
makes the seam honest: a behaviour one store has and the other lacks is a bug
in one of them. It found two — a shallow `slices.Clone` that let a reader mutate
the append-only log, and `MemStore` keeping nanoseconds that `timestamptz`
cannot (`d467540`, `a382a85`).

## Alternatives rejected

A per-conversation advisory lock would serialise writers correctly but costs a
round trip and holds server state for a sequence the primary key already
protects. Generating the sequence server-side with `max(seq)+1` was rejected
because the `Store` contract has the caller supply `seq` and the store reject
it — the orchestrator knows which sequence it reduced up to, and a store that
silently renumbers would hide a lost write.

## Forecloses

Changing the partition modulus needs a table rewrite, so 16 is deliberately
generous rather than tuned. A sequence beyond `int64` does not fit the column;
`Append` rejects it rather than wrapping.
