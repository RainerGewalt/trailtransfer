// Package events defines the canonical MQTT event schema (snake_case, hashable)
// and a publisher that stamps worker identity, timestamps and the result hash.
package events

const (
	TypeStatus       = "status"
	TypeProgress     = "progress"
	TypeResult       = "result"
	TypeHealth       = "health"
	TypeCapabilities = "capabilities"
	TypeLog          = "log"
)

// Evidence event types in the Trail Evidence Record v1 namespace. Only
// terminal results are sealed: a job that ran ends in transfer.job.result, a
// job that was turned away before running ends in transfer.job.rejected.
const (
	EvidenceJobResult   = "transfer.job.result"
	EvidenceJobRejected = "transfer.job.rejected"
)

// StatusEvent marks a job lifecycle transition.
type StatusEvent struct {
	EventType string `json:"event_type"`
	JobID     string `json:"job_id"`
	WorkerID  string `json:"worker_id"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Timestamp string `json:"timestamp"`
}

// ProgressEvent is a lossy, high-volume progress sample (QoS 0).
type ProgressEvent struct {
	EventType        string  `json:"event_type"`
	JobID            string  `json:"job_id"`
	WorkerID         string  `json:"worker_id"`
	FilesTotal       int     `json:"files_total"`
	FilesTransferred int     `json:"files_transferred"`
	BytesTotal       int64   `json:"bytes_total"`
	BytesTransferred int64   `json:"bytes_transferred"`
	Percent          float64 `json:"percent"`
	Timestamp        string  `json:"timestamp"`
}

// ResultEvent is the receipt for a job. It is the basis for
// downstream evidence. result_hash is computed over this struct with the field
// left empty (it is omitempty), then filled in before publishing.
type ResultEvent struct {
	EventType        string   `json:"event_type"`
	JobID            string   `json:"job_id"`
	WorkerID         string   `json:"worker_id"`
	Action           string   `json:"action,omitempty"`
	Source           string   `json:"source,omitempty"`
	Target           string   `json:"target,omitempty"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason,omitempty"`
	Detail           string   `json:"detail,omitempty"`
	Justification    string   `json:"justification,omitempty"` // human "why", from command.reason
	StartedAt        string   `json:"started_at,omitempty"`
	FinishedAt       string   `json:"finished_at,omitempty"`
	DurationMS       int64    `json:"duration_ms,omitempty"`
	FilesTotal       int      `json:"files_total"`
	FilesTransferred int      `json:"files_transferred"`
	BytesTransferred int64    `json:"bytes_transferred"`
	Errors           []string `json:"errors"`
	RcloneExitCode   *int     `json:"rclone_exit_code,omitempty"`
	PolicyVersion    string   `json:"policy_version"`
	PolicyHash       string   `json:"policy_hash,omitempty"`
	WorkerVersion    string   `json:"worker_version"`
	CommandHash      string   `json:"command_hash,omitempty"`
}

// LogEvent is a single job-scoped log line.
type LogEvent struct {
	EventType string `json:"event_type"`
	JobID     string `json:"job_id"`
	WorkerID  string `json:"worker_id"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// HealthEvent is the retained heartbeat.
type HealthEvent struct {
	EventType        string   `json:"event_type"`
	Status           string   `json:"status"`
	WorkerID         string   `json:"worker_id"`
	Version          string   `json:"version"`
	UptimeSeconds    int64    `json:"uptime_seconds"`
	ActiveJobs       int      `json:"active_jobs"`
	MaxParallelJobs  int      `json:"max_parallel_jobs"`
	RcloneAvailable  bool     `json:"rclone_available"`
	PolicyVersion    string   `json:"policy_version"`
	PolicyHash       string   `json:"policy_hash,omitempty"`
	SupportedActions []string `json:"supported_actions"`
	Timestamp        string   `json:"timestamp"`
}

// CapabilitiesEvent is the retained description of what the worker can do.
type CapabilitiesEvent struct {
	EventType        string   `json:"event_type"`
	WorkerID         string   `json:"worker_id"`
	Version          string   `json:"version"`
	SupportedActions []string `json:"supported_actions"`
	MaxParallelJobs  int      `json:"max_parallel_jobs"`
	RcloneAvailable  bool     `json:"rclone_available"`
	EvidenceSchema   string   `json:"evidence_schema"`
	EvidenceJournal  bool     `json:"evidence_journal"`
	EvidenceSigned   bool     `json:"evidence_signed"`
	PolicyVersion    string   `json:"policy_version"`
	PolicyHash       string   `json:"policy_hash,omitempty"`
	Timestamp        string   `json:"timestamp"`
}
