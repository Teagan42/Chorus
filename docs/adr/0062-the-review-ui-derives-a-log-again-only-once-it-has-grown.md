# 0062. The review UI derives a log again only once it has grown

- **Status:** accepted
- **Source:** SPEC §8, §9.1 · ADR-0007, ADR-0026, ADR-0052

The review UI derives every pair, signal and summary from the journal on
read (ADR-0026, ADR-0052), and it did so by reading every log in the
household on every request. Browse and Triage each read a log three or four
times over, once per derivation, so a screen cost the whole journal's
history however little of it had changed. A household's journal only
grows, and most of it is days old.

Now `cmd/reviewui` keeps what each log derives (its harvest, its signals
and weak positives, Browse's summary of it, a satellite's wakes, presence
and wake negatives) beside the seq it was read to. Each request lists the
logs, asks the store for each one's last seq, and reads and derives again
only the logs whose last seq moved. The log is append-only (SPEC §8): a log
that has not grown is the same events, and every derivation is a fold over
them with no clock, so it derives the same thing. The store stays the
source of truth. An append moves the last seq before any later request
asks, so no request is served a log older than the store held when it
started.

What a reviewer writes is not kept: verdicts, labels, promotions and wake
verdicts are read on every request, as before, and laid over the kept
derivations. A kept derivation is shared between requests and never
changed once made, so concurrent screens share it as it is.

## Alternatives rejected

- **Expiry by time.** A cache that lives for a few seconds serves a pair
  that has been cut, or misses one that was just cut, for that long. The
  last seq says exactly when a log changed, at the cost of one small query
  per log.
- **Writing derived rows to the database.** A table of pairs or signals is a
  copy of the log that can drift from it, which ADR-0026 rejected for pairs.
  Kept in memory, the derivations go when the process does, and the next
  start reads the log again.
- **Paging Browse by day.** Browse already draws one day at a time
  (`?day=`); what it cost was reading every day's logs to find that one.

## Forecloses

The process holds a derivation for every log it has listed, so its memory
grows with the journal's length rather than with one request's. Retention
(SPEC §8) that deletes a log must also drop what was kept of it; nothing
deletes one today.
