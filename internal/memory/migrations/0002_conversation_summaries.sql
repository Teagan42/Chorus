-- What each conversation was about, kept for each identified person in it
-- (SPEC §5, ADR-0041). Private to that person: everyone it is kept for was
-- there. A resumed conversation that ends again replaces its summary.
CREATE TABLE conversation_summaries (
    conversation_id text        NOT NULL,
    person          text        NOT NULL,
    summary         text        NOT NULL,
    heard_at        timestamptz NOT NULL,

    PRIMARY KEY (conversation_id, person),
    CONSTRAINT conversation_summaries_person CHECK (person <> ''),
    CONSTRAINT conversation_summaries_summary CHECK (btrim(summary) <> '')
);

-- Every turn recalls one person's most recent conversations, and keeping one
-- prunes that person's oldest.
CREATE INDEX conversation_summaries_by_person ON conversation_summaries (person, heard_at DESC);
