package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sentinel/internal/agent"
	"sentinel/internal/stream"
)

// streamAgent replays Redis Stream history and then forwards live Pub/Sub
// messages. Subscribing before replay closes the gap between history and live
// delivery; duplicate live entries are filtered by their Stream ID.
func streamAgent(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := agentID(c)
		if !ok {
			return
		}
		if deps.Streams == nil {
			errorJSON(c, http.StatusServiceUnavailable, "streaming_disabled", "streaming is not configured")
			return
		}
		record, err := deps.Agents.LoadAgent(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, agent.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "agent_not_found", "Agent not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load Agent for streaming", "agent_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_load_failed", "failed to load the Agent")
			return
		}
		lastID := strings.TrimSpace(c.GetHeader("Last-Event-ID"))
		if lastID == "" {
			lastID = strings.TrimSpace(c.Query("last_event_id"))
		}
		if lastID != "" && !stream.ValidID(lastID) {
			errorJSON(c, http.StatusBadRequest, "invalid_last_event_id", "Last-Event-ID must be a Redis Stream ID")
			return
		}
		live, cancel, err := deps.Streams.Subscribe(c.Request.Context(), tenantID(c), id)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "subscribe to Agent stream", "agent_id", id, "error", err)
			errorJSON(c, http.StatusServiceUnavailable, "stream_unavailable", "failed to subscribe to the Agent stream")
			return
		}
		defer cancel()

		// http.Server.WriteTimeout is meant for bounded JSON responses. It is an
		// absolute deadline, so it would close every SSE connection after the
		// configured duration even while heartbeats are being sent. Clear it for
		// this response only; the handler still stops on client cancellation,
		// terminal events, or server shutdown.
		if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
			logger.WarnContext(c.Request.Context(), "failed to clear SSE write deadline", "agent_id", id, "error", err)
		}

		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			return
		}
		write := func(message stream.Message) error {
			if _, err := fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n",
				message.ID, message.Event.Type, message.Payload); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		if _, err := io.WriteString(c.Writer, "retry: 3000\n: connected\n\n"); err != nil {
			return
		}
		flusher.Flush()

		lastSent := lastID
		terminalSeen := false
		historyLast, err := deps.Streams.History(c.Request.Context(), tenantID(c), id, lastID, func(message stream.Message) error {
			if err := write(message); err != nil {
				return err
			}
			lastSent = message.ID
			terminalSeen = terminalSeen || message.Event.Terminal()
			return nil
		})
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "replay Agent stream", "agent_id", id, "error", err)
			return
		}
		if historyLast != "" {
			lastSent = historyLast
		}
		if terminalSeen || terminalAgent(record.Status) {
			// Deliver anything published while the history was replayed, then
			// close; a terminal Agent cannot produce further events.
			for {
				select {
				case message, ok := <-live:
					if !ok {
						return
					}
					if stream.CompareIDs(message.ID, lastSent) <= 0 {
						continue
					}
					if err := write(message); err != nil {
						return
					}
					lastSent = message.ID
					if message.Event.Terminal() {
						return
					}
				default:
					return
				}
			}
		}

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case message, ok := <-live:
				if !ok {
					return
				}
				if stream.CompareIDs(message.ID, lastSent) <= 0 {
					continue
				}
				if err := write(message); err != nil {
					return
				}
				lastSent = message.ID
				if message.Event.Terminal() {
					return
				}
			case <-ticker.C:
				if _, err := io.WriteString(c.Writer, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func terminalAgent(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}
