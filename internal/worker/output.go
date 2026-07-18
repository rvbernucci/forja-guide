package worker

import (
	"bytes"
	"sync"
	"sync/atomic"
	"time"
)

type outputBudget struct {
	maximum      int64
	total        atomic.Int64
	lastActivity atomic.Int64
	exceeded     chan struct{}
	onActivity   func(string, int)
	once         sync.Once
}

func newOutputBudget(maximum int, onActivity func(string, int)) *outputBudget {
	budget := &outputBudget{
		maximum:    int64(maximum),
		exceeded:   make(chan struct{}),
		onActivity: onActivity,
	}
	budget.touch()
	return budget
}

func (b *outputBudget) touch() {
	b.lastActivity.Store(time.Now().UnixNano())
}

func (b *outputBudget) inactiveFor(now time.Time) time.Duration {
	return now.Sub(time.Unix(0, b.lastActivity.Load()))
}

func (b *outputBudget) signalExceeded() {
	b.once.Do(func() { close(b.exceeded) })
}

type boundedStream struct {
	budget *outputBudget
	name   string
	mu     sync.Mutex
	data   bytes.Buffer
}

func (s *boundedStream) Write(p []byte) (int, error) {
	s.budget.touch()
	if s.budget.onActivity != nil {
		s.budget.onActivity(s.name, len(p))
	}
	previous := s.budget.total.Add(int64(len(p))) - int64(len(p))
	remaining := s.budget.maximum - previous
	if remaining > 0 {
		count := len(p)
		if int64(count) > remaining {
			count = int(remaining)
		}
		s.mu.Lock()
		_, _ = s.data.Write(p[:count])
		s.mu.Unlock()
	}
	if previous+int64(len(p)) > s.budget.maximum {
		s.budget.signalExceeded()
	}
	return len(p), nil
}

func (s *boundedStream) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.data.Bytes())
}
