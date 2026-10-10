-- Every re-run a reviewer asked on Replay, promoted or not (ADR-0059): what
-- it ran under and what each turn did. A new run is a new row.
CREATE TABLE curation_reruns (
    id               bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id  text        NOT NULL,
    model            text        NOT NULL,
    prompt_version   text        NOT NULL,
    tool_schema      text        NOT NULL,
    system_prompt    text        NOT NULL,
    tool_schema_json text        NOT NULL,
    takes_json       jsonb       NOT NULL,
    ran_at           timestamptz NOT NULL,

    -- A run that reached no turn has nothing to promote or compare.
    CONSTRAINT curation_rerun_ran_a_turn CHECK (jsonb_array_length(takes_json) > 0)
);

CREATE INDEX curation_reruns_newest ON curation_reruns (conversation_id, ran_at DESC, id DESC);

-- A promoted take keeps the tool declarations it was offered beside its
-- prompt. Text, not jsonb, so it reads back as the reviewer saw it; a row
-- promoted before this has only the version.
ALTER TABLE curation_promotions ADD COLUMN tool_schema_json text NOT NULL DEFAULT '';
