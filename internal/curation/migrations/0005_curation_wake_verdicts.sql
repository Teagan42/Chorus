-- A reviewer's word on a stage-two wake rejection, the hard negative SPEC
-- §9.3 auto-labels, keyed by its device log and seq. Unreviewed is the
-- absence of a row.
CREATE TABLE curation_wake_verdicts (
    conversation_id text        NOT NULL,
    event_seq       bigint      NOT NULL,
    status          text        NOT NULL,
    judged_at       timestamptz NOT NULL,
    PRIMARY KEY (conversation_id, event_seq),

    CONSTRAINT curation_wake_status CHECK (status IN ('confirmed', 'discarded'))
);
