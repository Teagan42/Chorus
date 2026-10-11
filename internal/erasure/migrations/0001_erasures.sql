-- What a reviewer deleted, when, and how much (ADR-0070). Never what it
-- held: a row names the conversation, the clip or the memory by id, and
-- counts what went with it. Beside the journal, not in it: the log a
-- deletion removes cannot carry the record that it was removed.
CREATE TABLE erasures (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind            text        NOT NULL,
    conversation_id text        NOT NULL,
    subject         text        NOT NULL,
    person          text        NOT NULL,
    counted         text        NOT NULL,
    erased_at       timestamptz NOT NULL,

    CONSTRAINT erasures_kind CHECK (kind IN ('conversation', 'audio', 'memory', 'summary')),
    CONSTRAINT erasures_conversation CHECK (conversation_id <> '')
);

-- The review UI lists them newest first.
CREATE INDEX erasures_newest ON erasures (erased_at DESC, id DESC);
