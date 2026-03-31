package activity

import (
	"context"
	"time"

	sdkactivity "go.temporal.io/sdk/activity"

	"sentinel/internal/stream"
)

// publishEvent is best-effort. Redis streaming must never fail the Agent
// operation that produced the event.
func (a *Activities) publishEvent(ctx context.Context, tenantID, agentID, eventType string, stepID, attempt int64, data any) {
	if a.Streams == nil || tenantID == "" || agentID == "" {
		return
	}
	event, err := stream.NewEvent(eventType, stepID, attempt, data)
	if err != nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := a.Streams.Publish(publishCtx, tenantID, agentID, event); err != nil {
		if sdkactivity.IsActivity(ctx) {
			sdkactivity.GetLogger(ctx).Warn("failed to publish stream event", "type", eventType, "error", err)
		}
	}
}
