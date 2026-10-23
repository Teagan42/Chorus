-- The text behind every prompt and tool-schema version a chorusd offered its
-- model (ADR-0101). A label is a content hash, so a row is never updated:
-- the first write of a label is its text for good. The house's MCP tools
-- make a version no build can name, so this is where an export, an eval and
-- a re-run find what such a turn was offered.
CREATE TABLE context_versions (
    kind        text        NOT NULL CHECK (kind IN ('prompt', 'tool_schema')),
    version     text        NOT NULL,
    body        text        NOT NULL,
    -- The summary prompt a prompt version hashed beside the persona; empty
    -- for a tool schema.
    summary     text        NOT NULL DEFAULT '',
    recorded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, version)
);
