package web

import (
	"context"
	"sync"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/lock"
)

// Job states.
const (
	JobRunning = "running"
	JobDone    = "done"
	JobError   = "error"
)

// keepJobs is how many finished tests stay readable through GET /api/v1/tests/{id}.
const keepJobs = 20

// Job is a test started through the API (spec pkt 14.3).
type Job struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"` // "wan" | "lan-test"
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// Results are the history records the test wrote (one per link for WAN).
	Results []any  `json:"results"`
	Error   string `json:"error,omitempty"`
}

// jobManager runs one test at a time under the shared test lock (spec pkt 3.2).
type jobManager struct {
	lockFile string
	ctx      context.Context
	now      func() time.Time
	wg       sync.WaitGroup

	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
}

func newJobManager(ctx context.Context, lockFile string, now func() time.Time) *jobManager {
	return &jobManager{lockFile: lockFile, ctx: ctx, now: now, jobs: map[string]*Job{}}
}

// start takes the test lock without waiting and runs fn in the background.
// A *lock.BusyError means that another test (GUI, cron, CLI) is running.
func (m *jobManager) start(typ string, fn func(ctx context.Context) ([]any, error)) (Job, error) {
	l, err := lock.Acquire(m.lockFile, typ, 0)
	if err != nil {
		return Job{}, err
	}
	now := m.now().UTC()
	j := &Job{ID: history.NewID(now), Type: typ, State: JobRunning, StartedAt: now.Truncate(time.Second), Results: []any{}}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()
	snapshot := *j

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer l.Release()
		results, err := fn(m.ctx)
		fin := m.now().UTC().Truncate(time.Second)
		m.mu.Lock()
		defer m.mu.Unlock()
		j.FinishedAt = &fin
		if results != nil {
			j.Results = results
		}
		if err != nil {
			j.State, j.Error = JobError, err.Error()
		} else {
			j.State = JobDone
		}
		m.trim()
	}()
	return snapshot, nil
}

// trim forgets the oldest finished jobs. The caller holds m.mu.
func (m *jobManager) trim() {
	for len(m.order) > keepJobs {
		old := m.jobs[m.order[0]]
		if old != nil && old.State == JobRunning {
			return
		}
		delete(m.jobs, m.order[0])
		m.order = m.order[1:]
	}
}

// get returns a copy of the job.
func (m *jobManager) get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// wait blocks until every running test has ended or timeout passes.
func (m *jobManager) wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}
