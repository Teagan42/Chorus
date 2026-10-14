# 0091. The yes can be turned off, because the nonce dies faster than the model answers

- **Status:** accepted · refines [ADR-0038](0038-hold-a-call-for-the-persons-yes-and-redeem-it-from-the-log.md)
- **Source:** SPEC §6, §13 · ADR-0063, ADR-0086, ADR-0087

`journal.Redeemable` expires a nonce once `Heard > 1` -- once more than one
thing has been said since it was handed out -- so that a stale yes cannot
ride the answer to a later question. Measured 2026-10-13 that window is
shorter than the model's round trip. A turn on qwen3:14b takes 3.5 to 5.7 s
with thinking on; the household answers inside that, says something else, and
the model re-calls with a nonce that died in between. It is then permanently
one nonce behind:

    refused: used      presented cf_01c897e5  ->  new cf_7f4d31bf
    refused: expired   presented cf_71206661  ->  new cf_71a3ed8f
    refused: expired   presented cf_7f4d31bf  ->  new cf_cc9c2923

The third presents a nonce issued 58 seconds and two nonces earlier. The cap
from ADR-0086 bounds this, but bounds it into failure: the call never runs,
however many times the household says yes.

`CHORUS_CONFIRM=off` runs a call that would be held as written, with no nonce
and no question. Unset or `on` holds it, so the safe behaviour is the one
nobody configured, and a value that is neither is refused at startup rather
than guessed -- guessing wrong runs a call nobody asked for.

**What it gives up.** Everything ADR-0038 and ADR-0063 are for: a lock, a
cover or a garage door runs on the model's reading of the request, with no
second word from the household. That is the trade, and it is theirs. The
household here asked for the switch before this was debugged, and was right
that the asking was the problem rather than a thing to be fixed around.

**Not a repair of the expiry.** Widening the window is the other answer and it
has a concrete cost: the record already pins `Answer` to the *first* thing
said after the question, so a nonce surviving later utterances can be redeemed
on a yes the person has since talked past -- "yes", then "no, wait", then the
model redeems the yes. That is why `Heard > 1` is tight. Choosing between a
confirmation that cannot complete and one that can be redeemed stale is a
decision for whoever owns the house, so this adds a switch and leaves the rule
alone.

## Alternatives rejected

**Expiring on a clock instead of on utterances.** It does not separate the two
cases: the hazard is that the person changed their mind, which is measured in
what they said and not in how long it took them to say it.

**Invalidating a nonce only when a different call is held.** Closer to the
real hazard, and it does not help here -- the nonces above died with no
competing question at all, purely because the household kept talking while
the model thought.

## Forecloses

A household that turns the yes off has no per-tool escape: it is off for the
lights and off for the lock. `registry.Target` carries the domain and classes
that `ConfirmWhen` reads (ADR-0041), so tiering the switch by what a call acts
on is available and unbuilt -- the same tiering ADR-0087 recorded as declined.
