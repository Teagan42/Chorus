-- A promoted take may only call tools: a re-run that checks the right sensor
-- and says nothing until it answers is a chosen side (ADR-0054).
ALTER TABLE curation_promotions DROP CONSTRAINT curation_promotion_says_something;
ALTER TABLE curation_promotions ADD CONSTRAINT curation_promotion_does_something
    CHECK (btrim(speech) <> '' OR jsonb_array_length(calls_json) > 0);
