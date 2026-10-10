# 0045. Timers belong to the house, and an announcement is a session with no wake word

- **Status:** accepted
- **Source:** SPEC §4, §4.2, §4.5, §7, §8, §14 · ADR-0003, ADR-0022, ADR-0038

SPEC §14 left two Phase 2 items: timers, and announcements with
`start_conversation`. They share a problem. Both speak on a satellite when
nobody there asked. SPEC §4 already says what that is: proactive speech is
a session with no wake word. This ADR says how such a session opens and
closes. It also says where a timer lives between being set and going off.

```go
import (
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
	"github.com/teaganglenn/chorus/internal/timer"
)

// What the kitchen is given when Alan's oven timer goes off: what to say,
// why, and who set it where.
func ovenGoesOff(t journal.Timer) session.Announcement {
	return session.Announcement{
		Text: timer.Says(t), Source: session.SourceTimer, TimerID: t.ID,
		RequestedBy: t.Person, FromSatellite: t.Satellite, FromConversation: t.ConversationID,
	}
}
```

## Timers

**A timer belongs to the house, not the conversation.** Alan sets the oven
timer and walks away. The conversation ends, and the timer has to go off
anyway. So `timer_started`, `timer_cancelled` and `timer_finished` go to a
log of their own, `house:timers` (`journal.HouseTimers`). The reducer folds
it like any other log: `State.Timers` lists each timer and its status,
`State.Running()` lists the ones still running. A timer started twice, or
ended twice, is a contradiction, so replay fails on it.

**The log is the schedule.** At startup, `timer.Start` replays the house
log and arms every running timer on the injected clock. A timer set before
a restart still goes off at its `fires_at`, an absolute UTC instant
recorded when it was set. SPEC §14 still defers session survival across a
restart. A timer survives because it was never a session's.

**It goes off where it was set.** `timer_finished` records the outcome:

- `announced`, with the conversation it was said in, once some of it was
  heard. Queued is not heard: the session may end, or the speaker fail,
  before it plays, and `Announcement.Heard` says which.
- `unannounced`, when the satellite was gone or never played it. Either
  is tried again every 5 s (`timer.RetryEvery`), so a kitchen that redials
  after a restart still hears it, a few seconds late.
- `missed`, when the timer came due more than 5 minutes ago
  (`timer.DefaultGrace`). That only happens while chorusd was down. An
  oven timer read out half an hour late is worse than none.

Triage raises a failure on anything but `announced` (SPEC §7). A timer
nobody heard is a failure the household felt. The failure's utterance is
what the timer was for, and its speaker is who set it.

**Three tools.** All three detach, because setting or cancelling takes
effect the moment it is called. A barge-in keeps the result.

- `timer_start` takes seconds (up to 24 hours), a label and the sentence
  to say. Its result tells the model the id, the seconds left and what will
  be said.
- `timer_cancel` takes the id. A timer already going off cannot be
  cancelled; the call says so rather than claim it was stopped.
- `timer_list` lists every running timer in the house, with the seconds
  left, not a clock time.

The default sentence is *The oven timer is done.*, from the label, or
*Your timer is done.* with none.

## Announcements

**An announcement opens a session with no wake word.**
`Supervisor.Announce` opens it on a satellite, with `announced` set on
`session_opened`. `announcement_made` records what is said, why
(`timer` or `request`), and who asked from where. The speak call that says
it follows. The model is not asked anything, and the words are its own, so
the session has nothing to think about. It plays them and closes as
`announced` once they have played. If an announcement on a satellite that
is already speaking queues behind the first, both play before the close.

**`start_conversation` hands the room the mic.** An announcement that asks
a question leaves its session open, and the mic feeds it as if someone had
woken it. Whoever answers is heard in that conversation with no wake word.
The silence backstop closes it if nobody does (SPEC §4.5). The model is
told what it said, and whose question it carried. Ollama sees
`"announcement"` on that entry: its source, timer, who asked, from where,
and whether an answer is expected.

**One speaker, one speaking session.** A satellite with a session open
says the announcement in that session, queued behind whatever it is
saying (SPEC §4.2). The person in the room hears the timer, and their next
ask knows it went off. An announcement plays even after a barge-in cut the
turn's speech, whether it was queued before the cut or after. The cut
silences the turn it interrupted, not the house.
On a quiet satellite, a second announcement joins the first's session
until it closes.

**The mic is not a timer's to hear.** While a session that only announces
is speaking, a wake word on that satellite is ignored. Its words would
otherwise answer nothing. Once the announcement has played, the satellite
wakes as it always does. A wake that confirms during an announcement waits
for it to finish, so the person's session opens on a silent speaker.

**`announce` is the tool.** It takes the text, a room (matched to a
satellite's room or name, ignoring case) and `start_conversation`. With no
room it is a broadcast: every satellite but the one it was asked on. The
result names the rooms that heard it and the ones that are not connected.
An unknown room is refused with the rooms there are.

**Announcements are not turns.** An announcement's first frame is not a
turn's first audio, so the household's wait for an answer is not measured
off a timer (SPEC §11). Harvest and the re-runner skip announced speak
calls: no turn chose those words, so they are no side of a pair. The
summary of a conversation says what was said unasked, and why.

## The review UI

Browse lists an announcement as its own conversation, tagged
*announcement*, named by what it said and whom it was for. Its log shows
the session opening to announce, why it was said, the clip and the close.
The house log is no conversation and is not listed. Its events appear
in the conversation they name, beside the events around them: the timer
being set after the `timer_start` call, and its going off in the session
that said it. A timer nobody heard opens the house log from Triage.

## Considered and rejected

- **A timer per conversation.** It would end with the conversation, or the
  conversation would have to outlive everyone in it.
- **Home Assistant's timer entities.** They need a helper per timer,
  created ahead of time, and report going off to HA, not to the satellite
  that heard the ask. Chorus would still need its own way to speak.
- **Asking the model what to say when a timer goes off.** A model call
  between the timer and the speaker adds latency, and a timer already
  carries its sentence.
- **Opening a second session on a busy satellite.** Two sessions would
  speak over each other on one speaker.
- **Saying a missed timer whenever chorusd comes back.** *The oven timer
  is done* an hour late sends someone to a burnt oven.

## Forecloses

- **Timers are not shared to Home Assistant.** An HA dashboard does not
  show them, and an HA automation cannot react to one.
- **A timer goes off only where it was set.** Moving rooms does not move
  it; the conversation follows the person, the timer does not.
- **Announcements are not interruptible by a wake word.** A barge-in still
  cuts one in a woken session. On a quiet satellite, a long announcement
  plays out.
- **The tool descriptions are unmeasured.** The models tier asks qwen3:14b
  to set the oven timer, cancel it by id, and ask the kitchen a question.
  It has not run where this was written, which has no Ollama endpoint.
