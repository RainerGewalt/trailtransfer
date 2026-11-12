package events

import (
	"encoding/json"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/config"
	"github.com/RainerGewalt/trailtransfer/internal/evidence"
)

// MQTT is the minimal publish surface the publisher needs (satisfied by
// internal/mqtt.Client and by test fakes).
type MQTT interface {
	Publish(topic string, qos byte, retain bool, payload []byte) error
}

// Publisher stamps worker identity/version/timestamps onto events and routes
// each to its topic with the right QoS and retention.
type Publisher struct {
	mq     MQTT
	cfg    *config.Config
	ver    string
	sealer *evidence.Sealer
}

// NewPublisher builds a publisher bound to a client, config, worker version and
// the evidence sealer used to chain-link and journal result records.
func NewPublisher(mq MQTT, cfg *config.Config, ver string, sealer *evidence.Sealer) *Publisher {
	return &Publisher{mq: mq, cfg: cfg, ver: ver, sealer: sealer}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (p *Publisher) send(topic string, qos byte, retain bool, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = p.mq.Publish(topic, qos, retain, b)
}

// Status publishes a lifecycle transition (QoS 1).
func (p *Publisher) Status(jobID, status, detail string) {
	p.send(p.cfg.JobStatusTopic(jobID), 1, false, StatusEvent{
		EventType: TypeStatus, JobID: jobID, WorkerID: p.cfg.WorkerID,
		Status: status, Detail: detail, Timestamp: now(),
	})
}

// StatusAnswer answers a status query on the query's own status topic with
// the queried job's last known status.
func (p *Publisher) StatusAnswer(queryJobID, targetJobID, targetStatus string) {
	p.send(p.cfg.JobStatusTopic(queryJobID), 1, false, StatusEvent{
		EventType: TypeStatus, JobID: queryJobID, WorkerID: p.cfg.WorkerID,
		Status: "completed", Detail: "job " + targetJobID + ": " + targetStatus,
		TargetJobID: targetJobID, TargetStatus: targetStatus, Timestamp: now(),
	})
}

// Progress publishes a progress sample (QoS 0). Identity/timestamp are filled.
func (p *Publisher) Progress(jobID string, e ProgressEvent) {
	e.EventType, e.JobID, e.WorkerID, e.Timestamp = TypeProgress, jobID, p.cfg.WorkerID, now()
	p.send(p.cfg.JobProgressTopic(jobID), 0, false, e)
}

// Log publishes a job-scoped log line (QoS 0).
func (p *Publisher) Log(jobID, level, msg string) {
	p.send(p.cfg.JobLogsTopic(jobID), 0, false, LogEvent{
		EventType: TypeLog, JobID: jobID, WorkerID: p.cfg.WorkerID,
		Level: level, Message: msg, Timestamp: now(),
	})
}

// Result finalises worker identity/version, seals the result as a
// trail.evidence/1 record in the evidence hash chain (and journal), then
// publishes the sealed record (QoS 1). The caller fills the semantic fields;
// actor records who caused it.
//
// If the result cannot be sealed, nothing is published and the error is
// returned: a result travels only as a sealed record.
func (p *Publisher) Result(r ResultEvent, actor *evidence.Actor) error {
	r.EventType = TypeResult
	r.WorkerID = p.cfg.WorkerID
	r.WorkerVersion = p.ver
	if r.Errors == nil {
		r.Errors = []string{}
	}
	eventType := EvidenceJobResult
	if r.Status == "rejected" {
		eventType = EvidenceJobRejected
	}
	rec, err := p.sealer.Seal(eventType, r.JobID, r, actor)
	if err != nil {
		return err
	}
	_ = p.mq.Publish(p.cfg.JobResultTopic(r.JobID), 1, false, rec)
	return nil
}

// EvidenceErr returns the journal write error that stopped sealing, or nil.
func (p *Publisher) EvidenceErr() error { return p.sealer.Err() }

// Health publishes the retained heartbeat (QoS 1, retained).
func (p *Publisher) Health(e HealthEvent) {
	e.EventType, e.WorkerID, e.Version, e.Timestamp = TypeHealth, p.cfg.WorkerID, p.ver, now()
	p.send(p.cfg.HealthTopic(), 1, true, e)
}

// Capabilities publishes the retained capability description (QoS 1, retained).
func (p *Publisher) Capabilities(e CapabilitiesEvent) {
	e.EventType, e.WorkerID, e.Version, e.Timestamp = TypeCapabilities, p.cfg.WorkerID, p.ver, now()
	p.send(p.cfg.CapabilitiesTopic(), 1, true, e)
}
