// Package health publishes the periodic retained heartbeat so consumers (and
// TrailMQ) can tell a live worker from a quiet one, complementing the MQTT
// Last-Will that marks it offline on an unclean disconnect.
package health

import (
	"context"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/events"
	"github.com/RainerGewalt/trailtransfer/internal/policy"
	"github.com/RainerGewalt/trailtransfer/internal/rclone"
)

// Reporter exposes the live counters a heartbeat needs.
type Reporter interface {
	ActiveCount() int
	SupportedActions() []string
	// Health returns "healthy", or "unhealthy" with the reason the worker
	// refuses jobs.
	Health() (status, detail string)
}

// StartLoop publishes an immediate heartbeat and then one every interval until
// ctx is cancelled. rcloneBinary is probed each tick for availability.
func StartLoop(ctx context.Context, pub *events.Publisher, pol *policy.Policy, r Reporter, rcloneBinary string, interval time.Duration) {
	start := time.Now()
	emit := func() {
		status, detail := r.Health()
		pub.Health(events.HealthEvent{
			Status:           status,
			Detail:           detail,
			UptimeSeconds:    int64(time.Since(start).Seconds()),
			ActiveJobs:       r.ActiveCount(),
			MaxParallelJobs:  pol.MaxParallelJobs,
			RcloneAvailable:  rclone.Available(rcloneBinary),
			PolicyVersion:    pol.PolicyVersion,
			PolicyHash:       pol.Hash,
			SupportedActions: r.SupportedActions(),
		})
	}
	go func() {
		emit()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				emit()
			}
		}
	}()
}
