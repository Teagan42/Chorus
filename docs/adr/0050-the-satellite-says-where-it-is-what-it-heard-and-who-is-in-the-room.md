# 0050. The satellite says where it is, both of what it heard, and who is in the room

- **Status:** accepted
- **Source:** SPEC §3.2, §3.3.1, §5, §8, §9.3 · ADR-0006, ADR-0009, ADR-0030

The satellite knew three things the host threw away. Its room was in the
inventory, but no turn was told it, so "turn off the lights" could not mean
this room's (SPEC §5). Its second mic channel arrived on every link and was
dropped, though SPEC §8 keeps both channels and §9.3 builds the wake-word
corpus on them. And the Satellite1's mmWave radar was never read, so Browse
had a presence band in its kit and no data for it. None of this changes the
`chorus_bridge` wire protocol: the room is config, the second channel
already rides it, and presence is an ordinary native API entity.

```go
import (
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// Alice in the study, asking for the lights: the model is told the room
// the log recorded, and so is any replay of the turn.
func askedFromTheStudy(st journal.State) session.Input {
	return session.Input{Speaker: st.Speaker, Room: st.Room, Text: "and the lights"}
}
```

## The room is turn metadata, from the log

`session.Config.Rooms` maps each satellite to the inventory's `room:`.
`session_opened` records it as `room`, and `journal.State.Room` is the last
one. Every ask, and every rerun (`rerun.Turn.Room`), is told that room in
the system prompt, beside who is speaking and never in their words (SPEC
§5, ADR-0006). The room is the satellite's, whoever is speaking, so a
conversation that follows Alice to the study is told the study from her
next turn on. A satellite in no room records none, and the model is told
nothing rather than an empty location.

## The second channel is kept, and decides nothing

Recognition, the embedder and the endpointer all decide on channel 0, as
before. The listener now keeps channel 1 over the same span: the lead-in,
the utterance, and a wake's window. It is stored as its own blob beside the
first (`<key>.ch1`) and named by `second_audio_ref` on
`utterance_transcribed` and `wake_rejected`. A device streaming one channel
records no field at all. Mute drops it like everything else.

The two spans line up to the chunk, not the sample. Each channel's chunks
arrive as separate frames, and the listener keeps the channel 1 chunks that
arrive while channel 0's span is open. A wake window, for
instance, closes on channel 0's last chunk, one chunk before channel 1's.
That is 32 ms against clips a second or more long, and the corpus is for
retraining, which does not need sample alignment. What channel 1 carries is
the firmware's setting (SPEC §3.2); nothing here assumes it.

## Presence belongs to the room, in the device's log

On each native API connection chorusd lists the device's entities. The
first binary sensor whose device class is `occupancy` or `presence` is the
room's presence sensor: on the Satellite1 that is "Room Presence", which
FutureProofHomes' `satellite1_radar` registers once an LD2410 or LD2450
answers on the mmWave header. chorusd subscribes to states only when it
finds one, and journals `presence_changed` with `state` present, absent
or unknown to `device:<satellite>`, the log wake rejections already use.
It records a change, not every report.

Presence is nobody's conversation, so the session reducer folds it as no
state, and harvest passes over it. Browse draws a span from present to the
next absent or unknown, clipped to the day, and one still open runs to now.

**A drop is unknown, not absent.** When the native API connection drops
while someone is present, chorusd records `unknown`. Browse then shows a
gap where nothing was watching, rather than a person in the room through it
or a room declared empty. A daemon that dies without recording anything
leaves the span open until the next report after restart.

`esphome/satellite1.yaml` now includes the radar component, from the same
pinned release, on UART0's pins. The logger is on USB-Serial-JTAG, so those
pins are free. A board with no radar fitted lists no presence entity.

## Alternatives rejected

- **Interleaving both channels into one stereo blob.** Every reader of mic
  blobs (STT reruns, the review UI's player, harvest) expects mono, and the
  second channel is the minority use.
- **Aligning the second channel by byte offset since the link opened.** It
  is exact only while neither ring buffer drops a chunk, and ESPHome's ring
  overwrites silently (SPEC §3.3.2), so the precision would be a claim the
  host cannot check.
- **Matching the presence sensor by its object id, `room_presence`.** It is
  derived from a display name the vendor may change. The device class is
  what Home Assistant itself keys on.
- **Presence in the conversation's log.** It happens with nobody talking,
  and would have to be copied into every conversation the room hosts.

## Forecloses

- Presence is per satellite. A household with two radars in one room gets
  two lanes of it, not a merged room.
- chorusd reads the entity list once per connection, so a radar detected
  after the native API connected is not watched until the next connection.
- The native API now carries a state subscription on a Satellite1, and with
  it the device's state traffic. Driving the LED ring, ducking and the
  wake-sensitivity select are still to be built on the same connection.
