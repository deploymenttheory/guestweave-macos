// In-memory tracker for asynchronous long-running jobs (lume's pull tracker).
//go:build darwin

package httpapi

import (
	"strconv"
	"sync"
	"time"
)

// pullJobs tracks asynchronous pulls in memory.
type pullJobs struct {
	mutex sync.Mutex
	next  int
	jobs  map[string]*pullJob
}

type pullJob struct {
	id        string
	image     string
	status    string
	err       string
	startedAt time.Time
	endedAt   time.Time
}

func (p *pullJobs) start(image string, run func() error) string {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if p.jobs == nil {
		p.jobs = map[string]*pullJob{}
	}
	p.next++
	job := &pullJob{
		id:        strconv.Itoa(p.next),
		image:     image,
		status:    "running",
		startedAt: time.Now(),
	}
	p.jobs[job.id] = job

	go func() {
		err := run()
		p.mutex.Lock()
		defer p.mutex.Unlock()
		job.endedAt = time.Now()
		if err != nil {
			job.status = "failed"
			job.err = err.Error()
		} else {
			job.status = "succeeded"
		}
	}()
	return job.id
}

func (p *pullJobs) get(id string) (pullJobResponse, bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	job, ok := p.jobs[id]
	if !ok {
		return pullJobResponse{}, false
	}
	response := pullJobResponse{
		ID:        job.id,
		Image:     job.image,
		Status:    job.status,
		Error:     job.err,
		StartedAt: job.startedAt.UTC().Format(time.RFC3339),
	}
	if !job.endedAt.IsZero() {
		response.EndedAt = job.endedAt.UTC().Format(time.RFC3339)
	}
	return response, true
}
