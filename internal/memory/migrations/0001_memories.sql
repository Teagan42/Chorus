-- What each person asked to be remembered (SPEC §5, ADR-0040). A table
-- beside the journal, not events in it: forgetting deletes the row, and the
-- log keeps the remember call that made it as history.
CREATE TABLE memories (
    id              text        PRIMARY KEY,
    person          text        NOT NULL,
    fact            text        NOT NULL,
    shareable       boolean     NOT NULL,
    conversation_id text        NOT NULL,
    call_id         text        NOT NULL,
    remembered_at   timestamptz NOT NULL,

    CONSTRAINT memories_person CHECK (person <> ''),
    CONSTRAINT memories_fact CHECK (btrim(fact) <> '')
);

-- Every turn recalls one person's memories and the household's shared ones,
-- newest first.
CREATE INDEX memories_by_person ON memories (person, remembered_at DESC);
CREATE INDEX memories_shared ON memories (remembered_at DESC) WHERE shareable;
