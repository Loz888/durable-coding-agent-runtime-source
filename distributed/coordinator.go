package distributed

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrDuplicateWorker = errors.New("worker already registered")
	ErrUnknownWorker   = errors.New("unknown worker")
	ErrWorkerNotAlive  = errors.New("worker heartbeat expired")
	ErrDuplicateJob    = errors.New("job already exists")
	ErrJobNotFound     = errors.New("job not found")
	ErrLeaseLost       = errors.New("lease lost")
)

type JobState string

const (
	StatePending   JobState = "pending"
	StateLeased    JobState = "leased"
	StateSucceeded JobState = "succeeded"
)

type Job struct {
	ID         int64     `json:"id"`
	ToolName   string    `json:"tool_name"`
	Payload    string    `json:"payload"`
	State      JobState  `json:"state"`
	WorkerID   string    `json:"worker_id,omitempty"`
	LeaseUntil time.Time `json:"lease_until,omitempty"`
	Epoch      uint64    `json:"epoch"`
	Attempts   int       `json:"attempts"`
	Output     string    `json:"output,omitempty"`
}

type WorkerView struct {
	ID            string    `json:"worker_id"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Alive         bool      `json:"alive"`
}

type Coordinator struct {
	mu           sync.Mutex
	jobs         map[int64]Job
	workers      map[string]time.Time
	leaseTTL     time.Duration
	heartbeatTTL time.Duration
	now          func() time.Time
}

func NewCoordinator(leaseTTL, heartbeatTTL time.Duration) (*Coordinator, error) {
	if leaseTTL <= 0 || heartbeatTTL <= 0 {
		return nil, errors.New("TTL must be positive")
	}
	return &Coordinator{
		jobs: make(map[int64]Job), workers: make(map[string]time.Time),
		leaseTTL: leaseTTL, heartbeatTTL: heartbeatTTL, now: time.Now,
	}, nil
}

func (c *Coordinator) Register(id string) error {
	if id == "" {
		return errors.New("empty worker id")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.workers[id]; ok {
		return ErrDuplicateWorker
	}
	c.workers[id] = c.now()
	return nil
}

func (c *Coordinator) Heartbeat(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.workers[id]; !ok {
		return ErrUnknownWorker
	}
	c.workers[id] = c.now()
	return nil
}

func (c *Coordinator) Workers() []WorkerView {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	out := make([]WorkerView, 0, len(c.workers))
	for id, t := range c.workers {
		out = append(out, WorkerView{ID: id, LastHeartbeat: t, Alive: now.Before(t.Add(c.heartbeatTTL))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *Coordinator) Enqueue(id int64, toolName, payload string) (Job, error) {
	if id <= 0 || toolName != "uppercase" || len(payload) == 0 || len(payload) > 1024 {
		return Job{}, errors.New("invalid job")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.jobs[id]; ok {
		return Job{}, ErrDuplicateJob
	}
	j := Job{ID: id, ToolName: toolName, Payload: payload, State: StatePending}
	c.jobs[id] = j
	return j, nil
}

func (c *Coordinator) Get(id int64) (Job, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.jobs[id]
	return j, ok
}

// Claim chooses the lowest-ID available job. A lease expiry makes a job reclaimable.
// All decisions occur under the same mutex: only one caller can claim a given epoch.
func (c *Coordinator) Claim(workerID string) (Job, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	heartbeat, ok := c.workers[workerID]
	if !ok {
		return Job{}, false, ErrUnknownWorker
	}
	if !now.Before(heartbeat.Add(c.heartbeatTTL)) {
		return Job{}, false, ErrWorkerNotAlive
	}
	var chosen int64
	found := false
	for id, j := range c.jobs {
		eligible := j.State == StatePending || (j.State == StateLeased && !now.Before(j.LeaseUntil))
		if eligible && (!found || id < chosen) {
			chosen, found = id, true
		}
	}
	if !found {
		return Job{}, false, nil
	}
	j := c.jobs[chosen]
	j.State = StateLeased
	j.WorkerID = workerID
	j.LeaseUntil = now.Add(c.leaseTTL)
	j.Epoch++
	j.Attempts++
	c.jobs[chosen] = j
	return j, true, nil
}

func (c *Coordinator) validLease(j Job, workerID string, epoch uint64, now time.Time) bool {
	return j.State == StateLeased && j.WorkerID == workerID && j.Epoch == epoch && now.Before(j.LeaseUntil)
}

func (c *Coordinator) Renew(id int64, workerID string, epoch uint64) (Job, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	now := c.now()
	if !c.validLease(j, workerID, epoch, now) {
		return Job{}, ErrLeaseLost
	}
	j.LeaseUntil = now.Add(c.leaseTTL)
	c.jobs[id] = j
	return j, nil
}

func (c *Coordinator) Complete(id int64, workerID string, epoch uint64, output string) (Job, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	if !c.validLease(j, workerID, epoch, c.now()) {
		return Job{}, ErrLeaseLost
	}
	j.State = StateSucceeded
	j.Output = output
	j.LeaseUntil = time.Time{}
	c.jobs[id] = j
	return j, nil
}
