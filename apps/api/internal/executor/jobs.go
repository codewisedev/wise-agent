package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"wise/apps/api/internal/secretguard"
)

// Job statuses.
const (
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// JobStatus is what callers poll.
type JobStatus struct {
	ID         string     `json:"id"`
	Workspace  string     `json:"workspace"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	Error      string     `json:"error,omitempty"`
	Output     string     `json:"output"`
	Offset     int64      `json:"offset"`     // absolute offset of Output's first byte
	NextOffset int64      `json:"nextOffset"` // pass back to continue
	Skipped    bool       `json:"skipped,omitempty"`
	More       bool       `json:"more,omitempty"` // output remains past NextOffset
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Result     any        `json:"result,omitempty"`
}

type job struct {
	mu       sync.Mutex
	st       JobStatus
	buf      []byte
	first    int64 // absolute offset of buf[0]
	max      int
	cancel   context.CancelFunc
	done     chan struct{}
	onCancel func()
}

// Write appends output, keeping the last max bytes.
func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.buf = append(j.buf, p...)
	if over := len(j.buf) - j.max; over > 0 {
		j.buf = append([]byte(nil), j.buf[over:]...)
		j.first += int64(over)
	}
	return len(p), nil
}

func (j *job) finish(status string, code *int, errMsg string, result any) {
	j.mu.Lock()
	if j.st.Status == JobRunning {
		j.st.Status, j.st.ExitCode, j.st.Error, j.st.Result = status, code, errMsg, result
		now := time.Now()
		j.st.FinishedAt = &now
	}
	j.mu.Unlock()
	close(j.done)
}

func (j *job) snapshot(offset int64, maxBytes int) JobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.st
	end := j.first + int64(len(j.buf))
	if offset < j.first {
		st.Skipped = offset > 0 || j.first > 0
		offset = j.first
	}
	if offset > end {
		offset = end
	}
	chunk := j.buf[offset-j.first:]
	if maxBytes > 0 && len(chunk) > maxBytes {
		chunk = chunk[:maxBytes]
	}
	// Return whole lines only (a credential split across two reads would
	// escape redaction), unless the job ended or a line is huge.
	if (st.Status == JobRunning || len(chunk) < len(j.buf[offset-j.first:])) && len(chunk) > 0 && chunk[len(chunk)-1] != '\n' {
		if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
			chunk = chunk[:i+1]
		} else if len(chunk) < 64<<10 {
			chunk = chunk[:0]
		}
	}
	st.Output = secretguard.Redact(string(chunk))
	st.Offset = offset
	st.NextOffset = offset + int64(len(chunk))
	st.More = st.NextOffset < end
	return st
}

type jobs struct {
	mu   sync.Mutex
	byID map[string]*job
	max  int
}

func newJobs() *jobs { return &jobs{byID: map[string]*job{}, max: 1 << 20} }

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (js *jobs) start(ws, kind string, run func(ctx context.Context, j *job) (int, any, error)) *job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{st: JobStatus{ID: newID(), Workspace: ws, Kind: kind, Status: JobRunning, StartedAt: time.Now()},
		max: js.max, cancel: cancel, done: make(chan struct{})}
	js.mu.Lock()
	js.gc()
	js.byID[j.st.ID] = j
	js.mu.Unlock()
	go func() {
		defer cancel()
		code, result, err := run(ctx, j)
		switch {
		case ctx.Err() != nil:
			j.finish(JobCancelled, nil, "cancelled", nil)
		case err != nil:
			var cp *int
			if code >= 0 {
				cp = &code
			}
			j.finish(JobFailed, cp, err.Error(), result)
		default:
			j.finish(JobSucceeded, &code, "", result)
		}
	}()
	return j
}

func (js *jobs) get(id string) *job {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.byID[id]
}

// gc drops jobs finished more than an hour ago (callers hold js.mu).
func (js *jobs) gc() {
	for id, j := range js.byID {
		j.mu.Lock()
		old := j.st.FinishedAt != nil && time.Since(*j.st.FinishedAt) > time.Hour
		j.mu.Unlock()
		if old {
			delete(js.byID, id)
		}
	}
}
