package publish

import (
	"fmt"
	"sync"
	"time"
)

// A progress reports how far along a long job is, at most once every
// interval, like
//
//	uploading files: 4210/12480 (34%), 5.2/s, about 26m left
//
// and, when it's done, the total and how long it took.
type progress struct {
	label    string
	total    int
	log      func(string)
	interval time.Duration

	mu          sync.Mutex
	count       int
	start, last time.Time
}

func newProgress(label string, total int, log func(string)) *progress {
	now := time.Now()
	return &progress{label: label, total: total, log: log, interval: 30 * time.Second, start: now, last: now}
}

func (p *progress) tick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	now := time.Now()
	if now.Sub(p.last) < p.interval {
		return
	}
	p.last = now

	rate := float64(p.count) / now.Sub(p.start).Seconds()
	left := time.Duration(float64(p.total-p.count) / rate * float64(time.Second))
	p.log(fmt.Sprintf("%s: %d/%d (%d%%), %.1f/s, about %s left",
		p.label, p.count, p.total, 100*p.count/p.total, rate, left.Round(time.Second)))
}

func (p *progress) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	elapsed := time.Since(p.start)
	rate := 0.0
	if elapsed > 0 {
		rate = float64(p.count) / elapsed.Seconds()
	}
	p.log(fmt.Sprintf("%s: %d in %s (%.1f/s)", p.label, p.count, elapsed.Round(time.Second), rate))
}
