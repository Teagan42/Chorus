package timer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teaganglenn/chorus/internal/session"
)

// view is a timer as the model is told it: seconds left, since a UTC
// instant beside the household's local time invites wrong arithmetic.
type view struct {
	TimerID     string `json:"timer_id"`
	Label       string `json:"label,omitempty"`
	Satellite   string `json:"satellite"`
	SecondsLeft int    `json:"seconds_left"`
}

// Tools are the timer_start, timer_cancel and timer_list executors.
func Tools(s *Scheduler) map[string]session.Tool {
	return map[string]session.Tool{
		"timer_start": session.ToolFunc(func(ctx context.Context, args string) (string, error) {
			var in struct {
				Seconds      int    `json:"seconds"`
				Label        string `json:"label"`
				Announcement string `json:"announcement"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			caller, _ := session.CallerFrom(ctx)
			t, err := s.Set(ctx, caller, Request{Seconds: in.Seconds, Label: in.Label, Announcement: in.Announcement})
			if err != nil {
				return "", err
			}
			return encode(struct {
				view
				Says string `json:"says"`
			}{s.view(t.ID, t.Label, t.Satellite, t.FiresAt), Says(t)})
		}),
		"timer_cancel": session.ToolFunc(func(ctx context.Context, args string) (string, error) {
			var in struct {
				TimerID string `json:"timer_id"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			caller, _ := session.CallerFrom(ctx)
			t, err := s.Cancel(ctx, caller, strings.TrimSpace(in.TimerID))
			if err != nil {
				return "", err
			}
			return encode(struct {
				Cancelled string `json:"cancelled"`
				Label     string `json:"label,omitempty"`
			}{t.ID, t.Label})
		}),
		"timer_list": session.ToolFunc(func(context.Context, string) (string, error) {
			out := struct {
				Timers []view `json:"timers"`
			}{Timers: []view{}}
			for _, t := range s.Running() {
				out.Timers = append(out.Timers, s.view(t.ID, t.Label, t.Satellite, t.FiresAt))
			}
			return encode(out)
		}),
	}
}

func (s *Scheduler) view(id, label, sat string, at time.Time) view {
	left := max(0, int(at.Sub(s.cfg.Clock.Now()).Round(time.Second)/time.Second))
	return view{TimerID: id, Label: label, Satellite: sat, SecondsLeft: left}
}

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(b), nil
}
