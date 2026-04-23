// Package stream publishes Agent progress events to Redis and replays them for
// SSE clients. Redis Streams are the durable history; Pub/Sub carries live
// delivery. The implementation speaks a minimal RESP2 subset so the runtime
// has no additional Redis client dependency.
package stream

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const (
	EventAgentStatus   = "agent.status"
	EventStepCompleted = "step.completed"
	EventModelStarted  = "model.started"
	// EventModelText carries the complete text of one model response. New
	// publishers emit it once instead of forwarding token deltas.
	EventModelText      = "model.text"
	EventModelArguments = "model.arguments"
	// EventModelDelta is retained for replaying streams produced before the
	// done-only streaming change. New publishers must not emit it.
	EventModelDelta       = "model.delta"
	EventModelCompleted   = "model.completed"
	EventModelFailed      = "model.failed"
	EventToolStarted      = "tool.started"
	EventToolCompleted    = "tool.completed"
	EventContextCompacted = "context.compacted"
	EventAgentCompleted   = "agent.completed"
	EventAgentFailed      = "agent.failed"
	EventAgentCancelled   = "agent.cancelled"
)

// Event is one immutable stream entry. Data is type-specific JSON.
type Event struct {
	Type      string          `json:"type"`
	StepID    int64           `json:"step_id,omitempty"`
	Attempt   int64           `json:"attempt,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func NewEvent(eventType string, stepID, attempt int64, data any) (Event, error) {
	event := Event{Type: eventType, StepID: stepID, Attempt: attempt, CreatedAt: time.Now().UTC()}
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return Event{}, err
		}
		event.Data = encoded
	}
	return event, nil
}

func (e Event) Terminal() bool {
	switch e.Type {
	case EventAgentCompleted, EventAgentFailed, EventAgentCancelled:
		return true
	}
	return false
}

// Message is a replayable stream entry. ID is the Redis Stream ID and is also
// used as the SSE event ID for Last-Event-ID reconnects.
type Message struct {
	ID      string
	Event   Event
	Payload []byte
}

type Publisher interface {
	Publish(ctx context.Context, tenantID, agentID string, event Event) (string, error)
}

type Reader interface {
	History(ctx context.Context, tenantID, agentID, afterID string, emit func(Message) error) (string, error)
	Subscribe(ctx context.Context, tenantID, agentID string) (<-chan Message, func(), error)
}

// ValidID reports whether value looks like a Redis Stream ID.
func ValidID(value string) bool {
	_, _, ok := splitID(value)
	return ok
}

// CompareIDs orders Redis Stream IDs numerically. Redis uses <ms>-<sequence>
// where both parts are unsigned integers, so string comparison is not enough.
func CompareIDs(left, right string) int {
	leftMs, leftSeq, leftOK := splitID(left)
	rightMs, rightSeq, rightOK := splitID(right)
	if !leftOK || !rightOK {
		return strings.Compare(left, right)
	}
	if leftMs < rightMs {
		return -1
	}
	if leftMs > rightMs {
		return 1
	}
	if leftSeq < rightSeq {
		return -1
	}
	if leftSeq > rightSeq {
		return 1
	}
	return 0
}

func splitID(value string) (int64, int64, bool) {
	msText, seqText, ok := strings.Cut(value, "-")
	if !ok {
		return 0, 0, false
	}
	ms, err := strconv.ParseInt(msText, 10, 64)
	if err != nil || ms < 0 {
		return 0, 0, false
	}
	seq, err := strconv.ParseInt(seqText, 10, 64)
	if err != nil || seq < 0 {
		return 0, 0, false
	}
	return ms, seq, true
}
