-- Reviewer verdicts on harvested pairs (SPEC §9.2). A separate table, not
-- journal events: the log is the runtime's append-only record, a verdict is
-- a person's revisable annotation of it, so the upsert below is the point.
CREATE TABLE curation_decisions (
    pair_id         text        PRIMARY KEY,
    conversation_id text        NOT NULL,
    status          text        NOT NULL,
    chosen          text        NOT NULL,
    unfixed         boolean     NOT NULL,
    reason          text        NOT NULL,
    prev_status     text        NOT NULL,
    decided_at      timestamptz NOT NULL,

    -- Unreviewed is the absence of a row; storing it would make "no decision"
    -- two states.
    CONSTRAINT curation_status CHECK (status IN ('edited', 'accepted', 'discarded')),
    CONSTRAINT curation_prev_status
        CHECK (prev_status IN ('', 'unreviewed', 'edited', 'accepted', 'discarded'))
);

-- The Curate page reads one conversation's decisions at a time.
CREATE INDEX curation_decisions_by_conversation
    ON curation_decisions (conversation_id);
