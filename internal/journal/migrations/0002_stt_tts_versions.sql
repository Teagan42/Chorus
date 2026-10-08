-- SPEC §8 pins every event to the configuration in effect. The model, prompt
-- and tool-schema columns attribute a completion; these attribute the ear
-- that produced a transcript and the voice that was cut by a barge-in, which
-- an eval otherwise cannot tell apart across a sidecar upgrade (ADR-0032).

-- Nullable, unlike the three before them: a row from before this migration
-- holds NULL, meaning "not recorded", while a journal that stamps an empty
-- slot writes '', meaning "no ear or voice was configured". The reader maps
-- both to an empty string; the column keeps the difference for SQL.
--
-- ALTER on the parent reaches every partition. No DEFAULT, so this is a
-- catalogue change on a table that may already hold a household's log.
ALTER TABLE journal_events
    ADD COLUMN stt_version text,
    ADD COLUMN tts_version text;

COMMENT ON COLUMN journal_events.stt_version IS 'STT model id behind the transcript; NULL predates this column';
COMMENT ON COLUMN journal_events.tts_version IS 'TTS model/voice behind the utterance; NULL predates this column';
