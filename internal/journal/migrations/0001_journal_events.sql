-- The journal is the runtime, not a trace of it (SPEC §8), so the invariants
-- the reducer depends on are enforced here and not only in Go.

-- HASH, not LIST: conversation ids are opaque and unbounded, so LIST would
-- need DDL per conversation. HASH keeps one conversation whole inside one
-- partition, which is the only access pattern - read a conversation's log.
CREATE TABLE journal_events (
    conversation_id     text        NOT NULL,
    seq                 bigint      NOT NULL,
    kind                text        NOT NULL,
    actor               text        NOT NULL,
    wall_clock          timestamptz NOT NULL,
    speculative         boolean     NOT NULL,
    audio_ref           text        NOT NULL,

    -- Columns, not JSONB: a persona change is A/B-tested by replaying the
    -- traces produced under one prompt version (SPEC §13).
    model_version       text        NOT NULL,
    prompt_version      text        NOT NULL,
    tool_schema_version text        NOT NULL,

    -- Taxonomy-dependent payload. Required fields vary per kind and are
    -- validated against the generated metadata before the write.
    fields              jsonb       NOT NULL,

    -- Rejects a duplicate sequence even when two writers raced past the Go
    -- check. The partition key must be part of the key, and is.
    PRIMARY KEY (conversation_id, seq),

    CONSTRAINT journal_events_seq_positive CHECK (seq > 0)
) PARTITION BY HASH (conversation_id);

-- 16 is sized for one household. Changing it later needs a rewrite, so it is
-- deliberately generous rather than tuned.
DO $$
BEGIN
    FOR i IN 0..15 LOOP
        EXECUTE format(
            'CREATE TABLE journal_events_p%s PARTITION OF journal_events '
            'FOR VALUES WITH (MODULUS 16, REMAINDER %s)', i, i);
    END LOOP;
END $$;

-- audio_ref points at the blob store; blobs never live in this table.
COMMENT ON COLUMN journal_events.audio_ref IS 'MinIO/filesystem key, never inlined audio';
