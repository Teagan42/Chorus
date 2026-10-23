# 0099. The Voices screen judges a clip at its satellite's accept level

- **Status:** accepted · refines [ADR-0098](0098-a-satellite-names-its-own-speaker-accept-level-and-click-ceiling.md)
- **Source:** SPEC §5, §9.2 · ADR-0080, ADR-0082, ADR-0084, ADR-0098

ADR-0098 foreclosed the Voices screen: it called a clip "under the
threshold" against `identity.DefaultAccept`, not the satellite's
`speaker_accept` nor `CHORUS_SPEAKER_ACCEPT`, because the review UI read
neither. On the Voice PE that ADR-0084 was measured on, every household
clip read as a guest on a screen whose whole job is choosing the takes
that make it not one. The review UI now reads both, and judges each clip
at its satellite's level: its own, else the house's, else 0.70, the order
chorusd reads them in.

```go norun
// cmd/reviewui: the level a clip from sat is judged at.
accept := s.voices.acceptFor(sat)
```

The inventory is `-devices`, or `CHORUS_DEVICES`, the same file chorusd
takes as `-devices`; docker-compose mounts it read-only, since it holds the
PSKs. Unset, every clip is judged at the house's level. Both values are
refused at startup as chorusd refuses them, so the screen never judges
against a number the daemon would not start with.

## Alternatives rejected

- **Asking chorusd for its levels.** The review UI and chorusd share a
  journal and a file, never a socket, and run fine one without the other.
  Reading the file chorusd reads is the same answer without the coupling.
