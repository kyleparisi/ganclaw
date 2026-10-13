// Package workers implements an isolated, outbound-polling build worker POC.
// It has no dependency on the gateway's bots, providers or scheduler.
package workers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const MaxBundle = 256 << 20

type Job struct {
	Warning    string    `json:"warning,omitempty"`
	Owner      string    `json:"owner,omitempty"`
	ID         string    `json:"id"`
	Worker     string    `json:"worker"`
	Profile    string    `json:"profile"`
	Revision   string    `json:"revision"`
	Timeout    int       `json:"timeout_seconds"`
	State      string    `json:"state"`
	Created    time.Time `json:"created"`
	Deadline   time.Time `json:"deadline,omitempty"`
	Seen       time.Time `json:"last_seen,omitempty"`
	Cancel     bool      `json:"cancel_requested"`
	Result     *Result   `json:"result,omitempty"`
	BundleSHA  string    `json:"bundle_sha256,omitempty"`
	BundleSize int64     `json:"bundle_size,omitempty"`
}

type Result struct {
	State    string `json:"state"`
	ExitCode int    `json:"exit_code"`
	Message  string `json:"message,omitempty"`
}

type Queue struct {
	mu   sync.Mutex
	dir  string
	lock *os.File
	jobs []Job
}

// OpenQueue holds an exclusive process lock and atomically persists each change.
func OpenQueue(dir string) (*Queue, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("state directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("worker state already in use")
	}
	q := &Queue{dir: dir, lock: f}
	b, err := os.ReadFile(filepath.Join(dir, "queue.json"))
	if err == nil {
		err = json.Unmarshal(b, &q.jobs)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.Close()
		return nil, err
	}
	return q, nil
}
func (q *Queue) Close() error { return q.lock.Close() }
func (q *Queue) save() error  { return atomicJSON(filepath.Join(q.dir, "queue.json"), q.jobs) }

func atomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func validID(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16 && s == hex.EncodeToString(b)
}
func terminal(s string) bool {
	return s == "succeeded" || s == "failed" || s == "cancelled" || s == "timed_out"
}
func (q *Queue) change(fn func() error) error {
	// Restore memory on persistence failure; don't acknowledge an uncommitted transition.
	before := append([]Job(nil), q.jobs...)
	if err := fn(); err != nil {
		return err
	}
	if err := q.save(); err != nil {
		q.jobs = before
		return err
	}
	return nil
}
func (q *Queue) Submit(j Job) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !validID(j.ID) || j.Worker == "" || j.Profile == "" || j.Timeout < 1 || j.Timeout > 7200 {
		return Job{}, errors.New("invalid job (ID must be 32 lowercase hex; timeout 1..7200)")
	}
	for _, old := range q.jobs {
		if old.ID == j.ID {
			if old.Worker != j.Worker || old.Profile != j.Profile || old.Revision != j.Revision || old.Timeout != j.Timeout {
				return Job{}, errors.New("job ID reused with different request")
			}
			return old, nil
		}
	}
	if len(q.jobs) >= 10000 {
		return Job{}, errors.New("POC queue limit reached")
	}
	j.Warning = ""
	j.Owner = ""
	j.State = "queued"
	j.Created = time.Now().UTC()
	j.Result = nil
	j.Cancel = false
	j.Deadline = time.Time{}
	j.Seen = time.Time{}
	j.BundleSHA = ""
	j.BundleSize = 0
	err := q.change(func() error { q.jobs = append(q.jobs, j); return nil })
	return j, err
}
func (q *Queue) Get(id string) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return Job{}, os.ErrNotExist
}
func (q *Queue) Poll(worker, owner string) (*Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !validID(owner) {
		return nil, errors.New("valid worker instance required")
	}
	for i := range q.jobs {
		j := &q.jobs[i]
		if j.Worker == worker && !terminal(j.State) && j.State != "queued" {
			if j.Owner != owner {
				return nil, errors.New("worker already assigned to another instance; no automatic takeover")
			}
			err := q.change(func() error { j.Seen = time.Now().UTC(); return nil })
			out := *j
			return &out, err
		}
	}
	for i := range q.jobs {
		j := &q.jobs[i]
		if j.Worker == worker && j.State == "queued" {
			err := q.change(func() error {
				j.Owner = owner
				j.State = "assigned"
				j.Seen = time.Now().UTC()
				j.Deadline = j.Seen.Add(time.Duration(j.Timeout) * time.Second)
				return nil
			})
			out := *j
			return &out, err
		}
	}
	return nil, nil
}
func (q *Queue) CancelJob(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.change(func() error {
		for i := range q.jobs {
			j := &q.jobs[i]
			if j.ID == id {
				j.Cancel = true
				if j.State == "queued" {
					j.State = "cancelled"
				}
				return nil
			}
		}
		return os.ErrNotExist
	})
}
func (q *Queue) Complete(id, worker, owner string, r Result) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.change(func() error {
		for i := range q.jobs {
			j := &q.jobs[i]
			if j.ID == id && j.Worker == worker && j.Owner == owner {
				if terminal(j.State) || j.State == "uncertain" {
					if j.Result != nil && *j.Result == r {
						return nil
					}
					return errors.New("result conflict")
				}
				if j.State != "assigned" || (!terminal(r.State) && r.State != "uncertain") {
					return errors.New("invalid transition")
				}
				if r.State != "uncertain" && j.BundleSHA == "" {
					return errors.New("upload artifacts before completing")
				}
				j.Result = &r
				j.State = r.State
				return nil
			}
		}
		return os.ErrNotExist
	})
}

// Resolve requires the operator to verify that no old job process/device session remains.
func (q *Queue) Resolve(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.change(func() error {
		for i := range q.jobs {
			j := &q.jobs[i]
			if j.ID == id {
				if j.State != "uncertain" {
					return errors.New("only uncertain jobs can be resolved")
				}
				j.State = "failed"
				return nil
			}
		}
		return os.ErrNotExist
	})
}
