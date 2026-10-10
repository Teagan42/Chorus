-- A reviewer's labels on a turn and what it should have done (SPEC §9.2),
-- keyed by the turn's utterance seq. Unannotated is the absence of a row.
CREATE TABLE curation_annotations (
    conversation_id text        NOT NULL,
    turn_seq        bigint      NOT NULL,
    labels          text[]      NOT NULL,
    should_have     text        NOT NULL,
    annotated_at    timestamptz NOT NULL,
    PRIMARY KEY (conversation_id, turn_seq),

    CONSTRAINT curation_annotation_labels CHECK (labels <@ ARRAY[
        'transcript_wrong', 'misunderstood_intent', 'wrong_tool', 'should_have_spoken',
        'spoke_when_it_shouldnt', 'too_slow', 'wrong_person', 'exemplar']::text[]),
    -- Storing an annotation that says nothing would make "unannotated" two states.
    CONSTRAINT curation_annotation_says_something
        CHECK (cardinality(labels) > 0 OR btrim(should_have) <> '')
);

-- A re-run's take promoted to the chosen side of a replay pair. The journal
-- never held it, so it is kept whole, with the prompt that produced it.
CREATE TABLE curation_promotions (
    conversation_id text        NOT NULL,
    turn_seq        bigint      NOT NULL,
    speech          text        NOT NULL,
    calls_json      jsonb       NOT NULL,
    model           text        NOT NULL,
    prompt_version  text        NOT NULL,
    tool_schema     text        NOT NULL,
    system_prompt   text        NOT NULL,
    promoted_at     timestamptz NOT NULL,
    PRIMARY KEY (conversation_id, turn_seq),

    CONSTRAINT curation_promotion_says_something CHECK (btrim(speech) <> '')
);
