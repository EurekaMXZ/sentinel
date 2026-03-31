package stream

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Addr     string
	Password string
	DB       int
	TTL      time.Duration
}

type Redis struct {
	client *respClient
	ttl    time.Duration
}

func NewRedis(ctx context.Context, cfg Config) (*Redis, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, errors.New("redis address is required")
	}
	if cfg.TTL <= 0 {
		return nil, errors.New("redis stream TTL must be positive")
	}
	client := newRESPClient(cfg.Addr, cfg.Password, cfg.DB)
	if _, err := client.do(ctx, "PING"); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Redis{client: client, ttl: cfg.TTL}, nil
}

func (r *Redis) Close() error { return r.client.Close() }

func streamKey(tenantID, agentID string) string {
	return "sentinel:stream:" + tenantID + ":" + agentID
}

func channelName(tenantID, agentID string) string {
	return "sentinel:stream:events:" + tenantID + ":" + agentID
}

func (r *Redis) Publish(ctx context.Context, tenantID, agentID string, event Event) (string, error) {
	if event.Type == "" {
		return "", errors.New("stream event type is required")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	key := streamKey(tenantID, agentID)
	id, err := r.client.string(ctx, "XADD", key, "*", "event", string(payload))
	if err != nil {
		return "", err
	}
	ttlSeconds := int64(r.ttl / time.Second)
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	if _, err := r.client.do(ctx, "EXPIRE", key, strconv.FormatInt(ttlSeconds, 10)); err != nil {
		return "", err
	}
	if _, err := r.client.do(ctx, "PUBLISH", channelName(tenantID, agentID), id+"\n"+string(payload)); err != nil {
		return "", err
	}
	return id, nil
}

// History replays entries after afterID in batches. The returned string is the
// last replayed stream ID. Redis returns entries in ascending ID order.
func (r *Redis) History(ctx context.Context, tenantID, agentID, afterID string, emit func(Message) error) (string, error) {
	const batch = 500
	key := streamKey(tenantID, agentID)
	start := "-"
	if afterID != "" {
		start = "(" + afterID
	}
	last := afterID
	for {
		reply, err := r.client.do(ctx, "XRANGE", key, start, "+", "COUNT", strconv.Itoa(batch))
		if err != nil {
			return last, err
		}
		entries, ok := reply.([]any)
		if !ok {
			return last, errors.New("invalid XRANGE reply")
		}
		for _, raw := range entries {
			entry, ok := raw.([]any)
			if !ok || len(entry) < 2 {
				return last, errors.New("invalid XRANGE entry")
			}
			id, _ := entry[0].(string)
			if id != "" {
				last = id
			}
			fields, ok := entry[1].([]any)
			if !ok {
				return last, errors.New("invalid XRANGE fields")
			}
			payload := fieldValue(fields, "event")
			if payload == "" {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				return last, err
			}
			if err := emit(Message{ID: id, Event: event, Payload: []byte(payload)}); err != nil {
				return last, err
			}
		}
		if len(entries) < batch {
			return last, nil
		}
		start = "(" + last
	}
}

func (r *Redis) Subscribe(ctx context.Context, tenantID, agentID string) (<-chan Message, func(), error) {
	raw, cancelRaw, err := r.client.subscribe(ctx, channelName(tenantID, agentID))
	if err != nil {
		return nil, nil, err
	}
	messages := make(chan Message, 256)
	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			cancelRaw()
		})
	}
	go func() {
		defer close(messages)
		for {
			select {
			case <-done:
				return
			case payload, ok := <-raw:
				if !ok {
					return
				}
				id, eventJSON, ok := strings.Cut(payload, "\n")
				if !ok {
					continue
				}
				var event Event
				if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
					continue
				}
				select {
				case messages <- Message{ID: id, Event: event, Payload: []byte(eventJSON)}:
				case <-done:
					return
				}
			}
		}
	}()
	return messages, cancel, nil
}

func fieldValue(fields []any, name string) string {
	for i := 0; i+1 < len(fields); i += 2 {
		key, _ := fields[i].(string)
		if key == name {
			value, _ := fields[i+1].(string)
			return value
		}
	}
	return ""
}
