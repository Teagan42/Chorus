# 0086. A transient is not speech, and a held call is given up on

- **Status:** accepted; its sustain floor superseded by [ADR-0090](0090-an-impulse-is-told-from-a-voice-by-its-shape-not-its-length.md), the held-call cap stands · refines [ADR-0038](0038-hold-a-call-for-the-persons-yes-and-redeem-it-from-the-log.md)
- **Source:** SPEC §4.5, §6 · ADR-0030, ADR-0063, ADR-0083, ADR-0085

One household conversation on 2026-10-13 held `ha_call_service` for a yes
thirteen times in three minutes. Two of those thirteen utterances were
spoken. The other eleven were **mouse clicks**: the ear read "Yeah." and
"No." in them, and each became a turn that re-asked the model.

**A transient is not speech, whatever the ear read in it.** The three clicks
measured peaked at -8 to -14 dBFS and held speech-level energy for 20, 40 and
120 ms, at a crest factor of 24 to 26 dB. Speech from the same room held 740
to 2520 ms at a crest factor of 14 to 20 dB. `listen.DefaultMinSustained` is
200 ms of windows at the satellite's own speech level (ADR-0083): above the
longest click, far below the shortest sentence. An utterance under it is
treated as silence, which is what it is. The decode still runs -- the floor is
on belief, not on cost -- and the rejection is logged with the words that were
not believed, so a floor set wrong in some other room is visible rather than
silent.

**A held call is given up on.** `journal.MaxHeldTries` is five. The loop above
could not converge for a second reason: the model presented nonces that were
never issued (`refused: args_changed`) and others already spent
(`refused: used`), and every refusal minted a fresh nonce. Nothing bounded
that. A model that will not echo the nonce it was handed cannot be made to,
so the session stops asking and tells it to say the call did not go through.

## Alternatives rejected

**Refusing the decode before asking the ear.** It saves an STT call per click
and loses the evidence: the logged "words that were not believed" are how a
household finds out the floor is wrong for their room, and ADR-0030 makes the
same argument for keeping the vad-stage rejections.

**Relaxing the guest refusal on a confirmation, as asked for.** An answer
nothing could identify is currently refused as `guest`, which makes a
question the assistant just asked an identified person unanswerable: a
one-word yes carries too little voice to attribute on this hardware
(ADR-0085). The relaxation was built and reverted. Dropping
`Unmatched(AnswerMatch)` and keeping only the wrong-person check lets a dinner
guest say yes to "unlock the front door" -- `TestAGuestsYesUnlocksNothing`
caught exactly that, with the lock call going through. The distinction the
check needs is not identified-versus-not but **what the call does**, and
`registry.Target` already carries the domain and classes that `ConfirmWhen`
uses to decide whether to hold a call at all (ADR-0041). Tiering the strictness
of a yes by the same target is the open decision; a flat relaxation is not
available.

## Forecloses

Nothing about a quiet speaker. The floor is sustained energy at the
satellite's own speech level, so a room whose level is set too high loses
quiet speech to it as well as clicks -- the two share one number, and
separating them would need a second measurement per room.
