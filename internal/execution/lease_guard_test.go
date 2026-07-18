package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestLeaseGuardCancelsExecutionWhenDeliveryRenewalFails(t *testing.T) {
	repository := &leaseGuardRepositoryStub{
		deliveryErr: errors.New("simulated stale delivery fence"),
	}
	fence := persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: "tenant", RepositoryID: "repository",
			ResourceType: "scheduler", ResourceID: "scheduler",
		},
		OwnerID: "owner", FencingToken: 3,
	}
	leaseSet := persistence.LeaseSet{
		LeaseSetID: "attempt", OwnerID: "delivery-owner",
		Leases: []persistence.Lease{{
			LeaseKey: persistence.LeaseKey{
				TenantID: "tenant", RepositoryID: "repository",
				ResourceType: "worktree", ResourceID: "delivery/attempt",
			},
			OwnerID: "delivery-owner", FencingToken: 4,
		}},
	}
	guard := startLeaseGuard(
		t.Context(), repository, fence, leaseSet, time.Minute, time.Millisecond,
	)
	select {
	case <-guard.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("lease renewal failure did not cancel execution")
	}
	_, err := guard.Stop()
	if err == nil || !strings.Contains(err.Error(), "stale delivery fence") ||
		repository.schedulerRenewals == 0 || repository.deliveryRenewals == 0 {
		t.Fatalf(
			"guard result err=%v scheduler=%d delivery=%d",
			err, repository.schedulerRenewals, repository.deliveryRenewals,
		)
	}
}

func TestLeaseGuardStopDoesNotReportIntentionalRenewalCancellation(t *testing.T) {
	started := make(chan struct{})
	repository := &leaseGuardRepositoryStub{
		blockScheduler:   true,
		schedulerStarted: started,
	}
	guard := startLeaseGuard(
		t.Context(), repository,
		persistence.LeaseProof{
			LeaseKey: persistence.LeaseKey{
				TenantID: "tenant", RepositoryID: "repository",
				ResourceType: "scheduler", ResourceID: "scheduler",
			},
			OwnerID: "owner", FencingToken: 3,
		},
		persistence.LeaseSet{LeaseSetID: "attempt", OwnerID: "delivery-owner"},
		time.Minute,
		time.Millisecond,
	)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lease renewal did not start")
	}
	if _, err := guard.Stop(); err != nil {
		t.Fatalf("intentional guard stop became a lease failure: %v", err)
	}
}

func TestLeaseGuardStopPreservesConcurrentRealRenewalFailure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	repository := &leaseGuardRepositoryStub{
		schedulerHook: func(context.Context) error {
			close(started)
			<-release
			return errors.New("simulated stale scheduler fence")
		},
	}
	guard := startLeaseGuard(
		t.Context(), repository,
		persistence.LeaseProof{
			LeaseKey: persistence.LeaseKey{
				TenantID: "tenant", RepositoryID: "repository",
				ResourceType: "scheduler", ResourceID: "scheduler",
			},
			OwnerID: "owner", FencingToken: 3,
		},
		persistence.LeaseSet{LeaseSetID: "attempt", OwnerID: "delivery-owner"},
		time.Minute,
		time.Millisecond,
	)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lease renewal did not start")
	}
	result := make(chan error, 1)
	go func() {
		_, err := guard.Stop()
		result <- err
	}()
	select {
	case <-guard.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("guard stop did not cancel renewal context")
	}
	close(release)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "stale scheduler fence") {
		t.Fatalf("concurrent renewal failure was hidden: %v", err)
	}
}

type leaseGuardRepositoryStub struct {
	schedulerRenewals int
	deliveryRenewals  int
	deliveryErr       error
	blockScheduler    bool
	schedulerStarted  chan struct{}
	schedulerHook     func(context.Context) error
}

func (s *leaseGuardRepositoryStub) RenewLease(
	ctx context.Context,
	_ persistence.LeaseKey,
	_ string,
	_ int64,
	_ time.Duration,
) (persistence.Lease, error) {
	s.schedulerRenewals++
	if s.schedulerHook != nil {
		return persistence.Lease{}, s.schedulerHook(ctx)
	}
	if s.blockScheduler {
		close(s.schedulerStarted)
		<-ctx.Done()
		return persistence.Lease{}, ctx.Err()
	}
	return persistence.Lease{}, nil
}

func (s *leaseGuardRepositoryStub) RenewLeaseSet(
	_ context.Context,
	leaseSet persistence.LeaseSet,
	_ time.Duration,
) (persistence.LeaseSet, error) {
	s.deliveryRenewals++
	return leaseSet, s.deliveryErr
}
