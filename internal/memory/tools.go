package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// errNobody is a call no identified person made. The session refuses those
// before they get here; this is the executor not trusting that it did.
var errNobody = errors.New("only an identified speaker has memories")

// Tools are the remember and forget executors. Whose memory it is comes from
// the call, never the arguments: a model that names the person could name
// anyone (SPEC §5).
func Tools(store Store, clock journal.Clock) map[string]session.Tool {
	return map[string]session.Tool{
		"remember": session.ToolFunc(func(ctx context.Context, args string) (string, error) {
			return remember(ctx, store, clock, args)
		}),
		"forget": session.ToolFunc(func(ctx context.Context, args string) (string, error) {
			return forget(ctx, store, args)
		}),
	}
}

func remember(ctx context.Context, store Store, clock journal.Clock, args string) (string, error) {
	caller, ok := session.CallerFrom(ctx)
	if !ok || caller.Person == "" {
		return "", errNobody
	}
	var in struct {
		Fact      string `json:"fact"`
		Shareable bool   `json:"shareable"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	m := Memory{
		ID: newID(), Person: caller.Person, Fact: strings.TrimSpace(in.Fact), Shareable: in.Shareable,
		ConversationID: caller.ConversationID, CallID: caller.CallID, At: clock.Now(),
	}
	if m.Fact == "" {
		return "", errors.New("there is no fact to remember")
	}
	if err := store.Remember(ctx, m); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"remembered":%q}`, m.ID), nil
}

func forget(ctx context.Context, store Store, args string) (string, error) {
	caller, ok := session.CallerFrom(ctx)
	if !ok || caller.Person == "" {
		return "", errNobody
	}
	var in struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	gone, err := store.Forget(ctx, caller.Person, strings.TrimSpace(in.MemoryID))
	if err != nil {
		return "", err
	}
	if !gone {
		// The same answer for a memory that is someone else's as for one
		// that does not exist: whose ids are whose is not the model's to
		// learn from a refusal.
		return "", fmt.Errorf("%s remembers nothing with id %q", caller.Person, in.MemoryID)
	}
	return fmt.Sprintf(`{"forgotten":%q}`, in.MemoryID), nil
}

func newID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("memory: crypto/rand failed: " + err.Error())
	}
	return "m_" + hex.EncodeToString(b[:])
}
