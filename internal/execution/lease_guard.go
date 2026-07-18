package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rvbernucci/forja-guide/internal/persistence"
)

var errLeaseGuardStopped = errors.New("delivery lease guard stopped")

type leaseRenewalRepository interface {
	RenewLease(
		context.Context,
		persistence.LeaseKey,
		string,
		int64,
		time.Duration,
	) (persistence.Lease, error)
	RenewLeaseSet(
		context.Context,
		persistence.LeaseSet,
		time.Duration,
	) (persistence.LeaseSet, error)
}

// leaseGuard keeps both scheduler and delivery authority alive. Losing either
// fence cancels the execution context so the process supervisor can terminate
// the worker before another author acquires overlapping authority.
type leaseGuard struct {
	repository leaseRenewalRepository
	fence      persistence.LeaseProof
	ttl        time.Duration
	interval   time.Duration

	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu       sync.RWMutex
	leaseSet persistence.LeaseSet
	err      error
}

func startLeaseGuard(
	parent context.Context,
	repository leaseRenewalRepository,
	fence persistence.LeaseProof,
	leaseSet persistence.LeaseSet,
	ttl time.Duration,
	interval time.Duration,
) *leaseGuard {
	ctx, cancel := context.WithCancelCause(parent)
	guard := &leaseGuard{
		repository: repository, fence: fence, leaseSet: leaseSet,
		ttl: ttl, interval: interval, ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	go guard.run()
	return guard
}

func (g *leaseGuard) Context() context.Context { return g.ctx }

func (g *leaseGuard) Snapshot() persistence.LeaseSet {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.leaseSet
}

func (g *leaseGuard) Err() error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.err
}

func (g *leaseGuard) Stop() (persistence.LeaseSet, error) {
	g.cancel(errLeaseGuardStopped)
	<-g.done
	return g.Snapshot(), g.Err()
}

func (g *leaseGuard) run() {
	defer close(g.done)
	timer := time.NewTimer(g.interval)
	defer timer.Stop()
	for {
		select {
		case <-g.ctx.Done():
			cause := context.Cause(g.ctx)
			if cause != nil && !errors.Is(cause, errLeaseGuardStopped) &&
				!errors.Is(cause, context.Canceled) {
				g.fail(cause)
			}
			return
		case <-timer.C:
			if err := g.renew(); err != nil {
				cause := context.Cause(g.ctx)
				// Stop and parent cancellation can interrupt an in-flight repository
				// call. Suppress only that cancellation-shaped error; a real stale
				// fence or database failure still wins even if shutdown raced it.
				if cause != nil &&
					(errors.Is(cause, errLeaseGuardStopped) || errors.Is(cause, context.Canceled)) &&
					errors.Is(err, context.Canceled) {
					return
				}
				g.fail(err)
				if cause == nil {
					g.cancel(err)
				}
				return
			}
			timer.Reset(g.interval)
		}
	}
}

func (g *leaseGuard) renew() error {
	deadline := min(g.interval, 5*time.Second)
	if deadline < 100*time.Millisecond {
		deadline = 100 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(g.ctx, deadline)
	defer cancel()
	if _, err := g.repository.RenewLease(
		ctx,
		g.fence.LeaseKey,
		g.fence.OwnerID,
		g.fence.FencingToken,
		g.ttl,
	); err != nil {
		return fmt.Errorf("renew scheduler fence: %w", err)
	}
	current := g.Snapshot()
	renewed, err := g.repository.RenewLeaseSet(ctx, current, g.ttl)
	if err != nil {
		return fmt.Errorf("renew delivery lease set: %w", err)
	}
	g.mu.Lock()
	g.leaseSet = renewed
	g.mu.Unlock()
	return nil
}

func (g *leaseGuard) fail(err error) {
	g.mu.Lock()
	if g.err == nil {
		g.err = err
	}
	g.mu.Unlock()
}

func deliveryHeartbeatInterval(ttl time.Duration) time.Duration {
	interval := ttl / 3
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	return interval
}
