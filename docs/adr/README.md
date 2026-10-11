# Architecture decision records

Hand-written, numbered, never edited after acceptance — superseded instead
(CONTRIBUTING §3). Every ADR cites the SPEC section or commit it was sourced
from; `docs/SPEC.md` stays normative and an ADR only records why a clause reads
the way it does. Start from [`0000-template.md`](0000-template.md).

These were backfilled from `docs/SPEC.md` and the commit log, grouped by
decision rather than by clause: facets of one choice share one record.

There is no 0028. The number was skipped when 0029 was written, and no record
numbered 0028 was ever committed or dropped.

| ADR | Decision | Source |
|---|---|---|
| [0001](0001-scope-single-household.md) | Target one household, not a product | §1 |
| [0002](0002-actor-supervisor-session.md) | Model a session as a supervisor, not a pipeline | §4 |
| [0003](0003-speak-is-a-tool.md) | Concurrent action stream; `speak` is a tool | §4.1, §12 |
| [0004](0004-barge-in-detection-gate.md) | Gate barge-in on speaker identity, not semantics | §4.3 |
| [0005](0005-interrupted-turn-truth.md) | Interrupted-turn truth and an exact truncation point | §4.4, §3.2.1 |
| [0006](0006-conversation-keyed-on-person.md) | Conversation keyed on person, audio on device | §4.5 |
| [0007](0007-journal-is-the-runtime.md) | The event journal is the source of truth | §8, §11 |
| [0008](0008-failures-are-tool-results.md) | Recoverable failures return to the model as tool results | §7 |
| [0009](0009-dial-out-native-api-client.md) | Be a native API client that dials the device | §3.1, §3.3.1 |
| [0010](0010-chorus-bridge-external-component.md) | Audio over a raw socket from an external component | §3.1, §3.2 |
| [0011](0011-registry-owns-policy.md) | Native tool registry owns policy; MCP is a provider | §6 |
| [0012](0012-cue-is-the-source-of-truth.md) | Author invariants in CUE; commit generated output | CONTRIBUTING §2 |
| [0013](0013-local-first-model-stack.md) | Local-first models behind capability-declaring providers | §10 |
| [0014](0014-turn-engine-cascade-first.md) | Turn-engine interface, cascade before S2S | §12, §11 |
| [0015](0015-two-stage-wake-confirmation.md) | Confirm the wake word server-side | §9.3 |
| [0016](0016-identity-and-memory.md) | Per-utterance attribution; memory sharing off by default | §5 |
| [0017](0017-monorepo-polyglot-by-seam.md) | One repo, polyglot by seam | §2, §13 |
| [0018](0018-mechanical-enforcement.md) | Enforce the five rules with tools, not review | CONTRIBUTING |
| [0019](0019-own-the-annotation-layer.md) | Own the annotation layer; OTel alongside | §9.1, §9.2 |
| [0020](0020-correct-device-api-facts.md) | Supersedes 0010: the socket and playback-position APIs as they exist | §3.1, §3.2.1 |
| [0021](0021-journal-sequence-enforced-in-postgres.md) | Hash-partition the journal; enforce its sequence in Postgres | §8, §13 |
| [0022](0022-one-live-session-per-conversation.md) | One live session per conversation; migration hands it over | §4.5, §8 |
| [0023](0023-kokoro-renders-at-24-khz-the-host-resamples.md) | Raw PCM from Kokoro, resampled 24→16 kHz on the host | §10, §3.2 |
| [0024](0024-partials-by-re-decoding-over-a-batch-endpoint.md) | STT partials by re-decoding a growing buffer over a batch endpoint; speaches pinned | §4.3, §4.5, §10 |
| [0025](0025-voiceprints-in-a-local-file-behind-a-minimal-embed-contract.md) | Voiceprints in a local file; one embed endpoint; placeholder thresholds (thresholds and model pin superseded by 0029; the embedding its Forecloses left out of the journal is recorded by 0030) | §5, §10, §13 |
| [0026](0026-harvest-barge-ins-as-uncurated-pairs.md) | Harvest a barge-in as an uncurated preference candidate | §9.1, §4.4, §8 |
| [0027](0027-home-assistant-service-call-detaches.md) | Act on the home through a detached Home Assistant service call | §6, §4.4, §14 |
| [0029](0029-speaker-id-on-onnx-runtime-pinned-by-digest.md) | Supersedes part of 0025: TitaNet-L on ONNX Runtime, pinned by asset digest, thresholds measured | §5, §10, §13 |
| [0030](0030-the-listening-child-owns-the-mic-stream.md) | The Listening child owns the mic stream, one per link; the endpointer seam; the embedding on the trace record | §4, §4.3, §4.5, §5, §9.3 |
| [0031](0031-skip-the-speaker-stage-when-nothing-identifies-speakers.md) | Refines 0004: skip the gate's speaker stage when nothing identifies speakers; the mode is the composition root's, never a candidate's | §4.3, §5 |
| [0032](0032-record-the-ear-and-the-voice-as-named-versions.md) | Record the STT model and the TTS model/voice as named version slots, nullable in Postgres, not gating a completion | §8 |
| [0033](0033-the-stop-is-tagged-and-its-answer-is-the-cut.md) | Refines 0005 and 0020: each stop is tagged and the device's echoed report is the truncation point; protocol version 2 | §4.4, §3.2.1, §15 |
| [0034](0034-curation-verdicts-live-beside-the-journal-not-in-it.md) | A reviewer's verdict on a harvested pair is a revisable row beside the journal, never a journal event | §8, §9.2 |
| [0035](0035-the-first-played-frame-is-an-event.md) | Journal the first frame the DAC plays of each turn, with the wait since the endpoint | §11, §8, §3.2.1, §9.2 |
| [0036](0036-smart-turn-judges-the-pause-energy-stays-the-floor.md) | Smart Turn v3.2, pinned through PyPI, judges each 200 ms pause; Energy's 800 ms stays the fallback | §4.5, §10, §11 |
| [0037](0037-the-model-is-asked-again-with-the-dialogue-from-the-log.md) | Ask the model again after each set of tool results, up to four asks, with the dialogue derived from the log | §4.1, §4.4, §7, §8 |
| [0038](0038-hold-a-call-for-the-persons-yes-and-redeem-it-from-the-log.md) | Hold a call for the person's yes; the log decides whether its nonce may run it | §6, §8 |
| [0039](0039-a-slow-tool-carries-what-to-say-while-it-works.md) | A slow tool carries what to say while it works; the session speaks it as the call starts | §4.1, §11, §14 |
| [0040](0040-memory-lives-beside-the-journal-and-each-turn-records-what-it-recalled.md) | Memory is a store beside the journal; person-scoped tools refuse a guest; each turn records what it recalled | §5, §8 |
| [0041](0041-hold-a-cover-that-lets-someone-in-by-its-device-class.md) | Refines 0038: hold a garage, gate or door cover by the `device_class` Home Assistant gives it, read before dispatch | §6 |
| [0042](0042-a-turn-that-ends-on-a-dangling-word-is-held.md) | Smart Turn's "finished" is checked against the words; a turn ending on *for*, *the*, *and* or a filler is held | §4.5, §11 |
| [0043](0043-each-conversation-is-summarized-for-the-people-in-it-and-each-turn-is-told-the-time.md) | Each conversation is summarized for the people in it when it ends; each turn is told the time and the last week's conversations | §5, §8 |
| [0044](0044-recall-chooses-by-relevance-and-recency-when-more-is-kept-than-a-turn-is-told.md) | Refines 0040 and 0043: past twenty memories or five conversations, an embedding model and recency choose what a turn is told | §5, §8, §11 |
| [0045](0045-timers-belong-to-the-house-and-an-announcement-is-a-session-with-no-wake-word.md) | Timers belong to the house, in a log of their own; an announcement is a session with no wake word, answerable with `start_conversation` | §4, §4.2, §7, §8 |
| [0046](0046-a-turn-that-calls-nothing-has-its-answer-spoken-from-its-content.md) | A turn that calls nothing, with its reasoning in the thinking field, has its content spoken when it ends | §4.1, §5 |
| [0047](0047-the-satellite-board-keeps-the-reference-voice-frontend-and-adds-a-wire.md) | Superseded by 0055. Proposed: Chorus's own satellite board keeps the Satellite1's ESP32-S3 and XU316 frontend, and adds PoE Ethernet, eight mics for direction of arrival, a speaker-sense return and a hardware mute | §3.2.1, §3.3.2, §4.3, §5 |
| [0048](0048-smart-turn-is-timed-by-the-clock-not-the-audio.md) | Refines 0036: Smart Turn's verdict is timed by the clock, so audio arriving in a burst after a radio stall waits for it, up to the hold | §3.3.2, §4.5 |
| [0049](0049-a-voice-that-matched-nobody-is-a-guest-and-the-voice-the-gate-refused-is-not-a-turn.md) | Refines 0040 and 0004: a voice judged to be nobody is a guest's turn, told nothing; the voice the gate refused while speech played is not a turn | §4.3, §5 |
| [0050](0050-the-satellite-says-where-it-is-what-it-heard-and-who-is-in-the-room.md) | Each turn is told the satellite's room, the second mic channel is kept beside the first, and the room's presence is journalled to the device log | §3.3.1, §5, §8, §9.3 |
| [0051](0051-a-failed-model-or-voice-is-said-and-kept-and-never-a-barge-in.md) | A model that is down, breaks or goes quiet past its deadline, and a voice that fails, are journalled and apologised for in one canned line; a voice failure is never a barge-in | §4.5, §7, §9.1 |
| [0052](0052-labels-and-promoted-re-runs-live-beside-the-journal-and-make-pairs-on-read.md) | Refines 0034: SPEC §9.2's labels and promoted re-runs live beside the journal; a labelled turn and a promoted take become pairs on read | §8, §9.2 |
| [0053](0053-direction-of-arrival-is-estimated-on-the-host-from-the-raw-array-and-the-reference.md) | Proposed: direction of arrival is estimated on the host, from the eight raw mics and the playback reference repacked into the existing I2S link and sent as an additive `array` frame | §3.2, §3.3.2, §4.3, §5, §8 |
| [0054](0054-a-pair-carries-what-each-side-called-beside-what-it-said.md) | Refines 0052: each side of a pair carries its tool calls beside its speech; a promoted re-run may be calls alone, and a *wrong tool* note trains on speech | §4.1, §9.2 |
| [0055](0055-the-satellite-is-a-bought-satellite1-kit-and-chorus-draws-only-the-board-under-it.md) | Supersedes 0047: the satellite is a bought FutureProofHomes Satellite1 kit; Chorus draws only the main board under its HAT, PoE Ethernet on pads the kit leaves free, checked pad by pad against the HAT's J7 | §3.3, §3.3.2, §5 |
| [0056](0056-an-interjection-pauses-the-voice-and-the-session-drives-the-ring.md) | `interject` pauses the playing line where the DAC stopped and resumes it after, journalled as `interjected`; each session's state is shown on the LED ring over the native API, by its effects | §3.2.1, §3.3.1, §4.2 |
| [0058](0058-a-rejected-wake-ships-in-its-own-corpus-and-a-repeat-is-evidence-on-its-first-ask.md) | Rejected wakes are a hard-negative corpus in their own file, shipped unless discarded, with `unknown_speaker` held for a confirm; weak positives are a Triage pile, not exported; a repeat is evidence on its first ask's annotation pair | §9.1, §9.3 |
| [0059](0059-every-re-run-is-kept-beside-the-journal-and-its-tool-schema-is-the-reviewers-to-edit.md) | Refines 0052: every re-run is kept beside the journal and promoted from there; the tool schema a re-run is offered is edited beside its prompt and versioned from the edit | §8, §9.2 |
| [0060](0060-a-timeout-leaves-outliving-work-running-and-each-edge-of-a-session-is-journalled.md) | Refines 0031 and 0049: a timeout cancels only `cancel` work; the model is offered what the build runs; a guest tool acts for nobody; a candidate names its utterance's one blob | §4.3, §4.4, §4.5, §5, §6, §9.3 |
| [0061](0061-direction-of-arrival-uses-the-kits-four-mics-and-is-estimated-on-the-satellite.md) | Proposed, amends 0053: direction of arrival uses the kit's four mics and is estimated on the ESP32-S3 by SRP-PHAT, sent as an additive `direction` frame; the raw array frame stays, opt-in, for journalling and tuning where the link carries it | §3.2, §3.3.2, §4.3, §5, §8 |
| [0062](0062-the-review-ui-derives-a-log-again-only-once-it-has-grown.md) | Refines 0026 and 0052: the review UI keeps what each log derives by the seq it was read to, and reads and derives a log again only once its last seq has moved; a reviewer's verdicts and labels are read on every request | §8, §9.1 |
| [0063](0063-the-yes-is-the-askers-and-a-guests-yes-runs-nothing.md) | Refines 0038 and 0041: a held call's nonce is redeemed only by the person who asked, when identified, and never by a voice that matched nobody; the generic `homeassistant` services are held by their target's domain and class, and `valve.open_valve` outright | §5, §6 |
| [0064](0064-a-hot-phrase-skips-the-word-count-never-the-speaker-and-is-not-answered.md) | Refines 0004: a closed, English-only set of hot phrases (stop, never mind, say that again) skips the gate's word count, never energy or the speaker; a stop that stopped something is not answered, never mind reaches a silent working turn, a repeat is said from the log, a stop silences a timer, and none of them is harvested as a pair | §4.3, §4.4, §9.1 |
| [0065](0065-retention-prunes-by-each-satellites-horizon-and-records-what-it-drops.md) | Refines 0007: `devices.yaml` sets audio and journal horizons in days, house-wide and per satellite, forever by default; chorusd prunes to them at startup and hourly, recording `audio_dropped` in the log that named each clip; curated work and a live session's log are kept; the house log replays from its oldest running timer; audio is not written below a free-space floor | §8, §4.5, §9, §13 |
| [0066](0066-the-device-proves-which-satellite-it-is-before-any-audio.md) | Refines 0010 and 0033: the audio link authenticates the device before any audio, by its node name and an HMAC over a fresh challenge under a key derived from its API PSK, from its inventory address; one live link per satellite; protocol version 3; encryption deferred | §3.2.2, §13 |

SPEC §15, the decisions that cannot be retrofitted, maps to ADRs 0003, 0005,
0006, 0007, 0011, and 0022. Those six carry the whole phase-1 risk.
