# 0015. Confirm the wake word server-side before the user can see it

- **Status:** accepted
- **Source:** SPEC §9.3, §9.4

The trained "Hey Eddie" model activates on coughs: recall is fine, precision is
not. So `micro_wake_word` activation on-device is stage one and is **not
user-visible**. The component sends a pre-roll and the orchestrator runs stage
two before any LED or chime — re-score the pre-roll at a higher threshold,
confirm the segment contains speech, confirm the speaker embedding matches a
household member. A cough fails all three.

Rejections are journalled with audio and auto-labeled as hard negatives, so the
retraining corpus fills itself with exactly the negatives the model lacks. Relief
is immediate and needs no retraining; dual-channel capture means the corpus
carries both AEC'd and raw streams, so retraining can target whichever frontend
ships.

## Consequences

False *rejects* are uncapturable under wake-gated streaming — no session, no
audio — so they are phase 2 and lower priority. The fix, if they surface, is a
rolling 3 s near-miss buffer uploaded only when confidence lands below the
activation threshold but above noise, which keeps the privacy story (§9.4).

Immediate relief without any code: the device exposes a
`select` **Wake word sensitivity** entity over the native API (§3.3.1).
