package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/RainerGewalt/trailtransfer/internal/evidence"
)

// runningJob is what the worker keeps on disk about a job between its
// admission and its sealed outcome. After a crash it is the only trace that
// the job was started.
type runningJob struct {
	CommandHash string `json:"command_hash,omitempty"`
	Action      string `json:"action"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Operator    string `json:"operator,omitempty"`
	Reason      string `json:"reason,omitempty"`
	DryRun      bool   `json:"dry_run"`
	StartedAt   string `json:"started_at"`
}

// stateStore persists the jobs that are running. Every change replaces the
// file atomically and is synced before the job continues, so a job that
// reached rclone is on disk. An empty path keeps the state in memory only.
type stateStore struct {
	path string
	mu   sync.Mutex
	jobs map[string]runningJob
}

func openState(path string) (*stateStore, error) {
	s := &stateStore{path: path, jobs: map[string]runningJob{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.jobs); err != nil {
		return nil, fmt.Errorf("job state %s is not readable: %w; move the file away to start without it, jobs that were running are then not reported as interrupted", path, err)
	}
	return s, nil
}

func (s *stateStore) snapshot() map[string]runningJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]runningJob, len(s.jobs))
	for k, v := range s.jobs {
		out[k] = v
	}
	return out
}

func (s *stateStore) add(jobID string, j runningJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[jobID] = j
	if err := s.persist(); err != nil {
		delete(s.jobs, jobID)
		return err
	}
	return nil
}

func (s *stateStore) remove(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[jobID]; !ok {
		return nil
	}
	delete(s.jobs, jobID)
	return s.persist()
}

func (s *stateStore) persist() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.Marshal(s.jobs)
	if err != nil {
		return err
	}
	return evidence.WriteFileAtomic(s.path, raw)
}
