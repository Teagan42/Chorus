# 0080. The review UI enrolls the household into the same file, and a browser take is made the satellite's format before it is sent

- **Status:** accepted · refines [ADR-0025](0025-voiceprints-in-a-local-file-behind-a-minimal-embed-contract.md)
- **Source:** SPEC §5, §9.2, §13 · ADR-0025, ADR-0029, ADR-0031, ADR-0070

Enrollment was a terminal tool: `cmd/enroll` embeds a person's guided
phrases over the speaker-ID sidecar and writes `identities.yaml`. Until
someone is enrolled every voice is a guest (ADR-0031), so the household's
first step past setup needed a terminal, WAV files already in the
satellite's format, and the right flags. The review UI is where a household
already reads what the assistant did and what it remembers about each
person; its Voices screen (`/voices`) now lists, enrolls and removes them.

**One file, one path through it.** The screen writes the same
`identities.yaml` chorusd reads, through the same code as `cmd/enroll`:
`identity.EnrollFile` loads the household or starts it for the sidecar's
model, embeds every take, and saves atomically, owner-readable only, only
once the enrollment is accepted; `identity.ParseWAV` is the one reader of a
take. Every take is read and checked before the first is embedded, so a
wrong file costs no sidecar call and a refused enrollment writes nothing.
The page lists id, name and how many phrases built each voiceprint, never
the vector (ADR-0025). chorusd still reads the file at startup only, so
every change says to restart it; a hot reload is its own decision.

**The server still resamples nothing.** A take uploaded to the screen is
held to the satellite's format, 16 kHz mono 16-bit PCM, and refused naming
what it is otherwise, as `cmd/enroll` refuses it. A take recorded on the
page is rendered to that format in the browser (`static/js/enroll.js`:
decoded, rendered to 16 kHz mono through an `OfflineAudioContext`, written
as 16-bit WAV) and sent in the same file input, so the server sees one kind
of take. The browser's echo cancelling, noise suppression and gain riding
are asked off: the satellite sends its own front end's output, and those
would only make the take less like it. A laptop's microphone is still not
a satellite's, which ADR-0029's thresholds were measured on, and the page
says a voiceprint from it matches less surely. The recorder shows only
where the browser can record (a secure context, which `127.0.0.1` is);
elsewhere the screen is an upload.

**What a reviewer may change.** An id goes on every transcript and into the
memory screen's paths, so it is one word of letters, digits, dots, dashes
or underscores; an id already enrolled is refused rather than replaced, as
`Identities.Enroll` refuses it. Removing a voiceprint is a deletion, behind
the second press and refused under `-read-only` like every other; enrolling
is not, and a read-only UI still enrolls. A removal is not a line in the
deletion trail: the trail is the journal's (ADR-0070), and the voiceprint
never was. With no `SPEAKERID_URL` the screen lists who is enrolled and says
why it enrolls nobody; with no household file it says that.
