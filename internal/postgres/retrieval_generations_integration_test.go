package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestRetrievalGenerationLifecycleDrainsBeforeRetirement(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	blue := retrievalGenerationFixture("blue", "v1")
	green := retrievalGenerationFixture("green", "v2")
	if err := store.RegisterRetrievalGeneration(t.Context(), blue); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterRetrievalGeneration(t.Context(), blue); err != nil {
		t.Fatalf("idempotent registration error = %v", err)
	}
	if previous, err := store.ActivateRetrievalGeneration(t.Context(), blue.GenerationID); err != nil || previous != nil {
		t.Fatalf("first activation previous=%#v err=%v", previous, err)
	}
	active, found, err := store.GetRetrievalGeneration(t.Context(), blue.GenerationID)
	if err != nil || !found || active.Status != "active" || active.ActivatedAt == nil {
		t.Fatalf("blue=%#v found=%v err=%v", active, found, err)
	}
	if err := store.RetireRetrievalGeneration(t.Context(), blue.GenerationID); err == nil {
		t.Fatal("active generation was retired")
	}
	if err := store.RegisterRetrievalGeneration(t.Context(), green); err != nil {
		t.Fatal(err)
	}
	previous, err := store.ActivateRetrievalGeneration(t.Context(), green.GenerationID)
	if err != nil || previous == nil || previous.GenerationID != blue.GenerationID || previous.Status != "active" {
		t.Fatalf("green activation previous=%#v err=%v", previous, err)
	}
	draining, found, err := store.GetRetrievalGeneration(t.Context(), blue.GenerationID)
	if err != nil || !found || draining.Status != "draining" {
		t.Fatalf("blue after drain=%#v found=%v err=%v", draining, found, err)
	}
	if err := store.RetireRetrievalGeneration(t.Context(), blue.GenerationID); err != nil {
		t.Fatal(err)
	}
	retired, found, err := store.GetRetrievalGeneration(t.Context(), blue.GenerationID)
	if err != nil || !found || retired.Status != "retired" || retired.RetiredAt == nil {
		t.Fatalf("retired=%#v found=%v err=%v", retired, found, err)
	}
}

func TestRetrievalGenerationRegistrationRejectsContractDrift(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	config := retrievalGenerationFixture("fixture", "v1")
	if err := store.RegisterRetrievalGeneration(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	drifted := config
	drifted.CollectionName = "forja_retrieval_other"
	if err := store.RegisterRetrievalGeneration(t.Context(), drifted); err == nil {
		t.Fatal("generation ID was reused with a different collection target")
	}
	drifted = config
	drifted.Dimensions = 4
	if err := store.RegisterRetrievalGeneration(t.Context(), drifted); err == nil {
		t.Fatal("generation ID was reused with a different vector contract")
	}
}

func TestRetrievalGenerationConcurrentActivationsLeaveOneActiveTarget(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	blue := retrievalGenerationFixture("blue", "v1")
	green := retrievalGenerationFixture("green", "v2")
	for _, config := range []persistence.RetrievalGenerationConfig{blue, green} {
		if err := store.RegisterRetrievalGeneration(t.Context(), config); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, generationID := range []string{blue.GenerationID, green.GenerationID} {
		go func(generationID string) {
			<-start
			_, err := store.ActivateRetrievalGeneration(t.Context(), generationID)
			errs <- err
		}(generationID)
	}
	close(start)
	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("concurrent activation error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent retrieval generation activation timed out")
		}
	}
	var active int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM forja.retrieval_generations
		WHERE tenant_id=$1 AND repository_id=$2
		  AND collection_alias='forja_retrieval' AND status='active'`,
		DefaultTenantID, DefaultRepositoryID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active retrieval generations=%d, want 1", active)
	}
}

func TestRetrievalAliasMutationGuardSerializesStoreInstances(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	first := newIntegrationStore(t, pool)
	second := newIntegrationStore(t, pool)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	errs := make(chan error, 2)

	go func() {
		errs <- first.WithRetrievalAliasMutation(t.Context(), "forja_retrieval", func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first alias mutation did not acquire its guard")
	}
	go func() {
		errs <- second.WithRetrievalAliasMutation(t.Context(), "forja_retrieval", func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second store entered the same alias mutation concurrently")
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseFirst)
	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("alias mutation guard error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("alias mutation guard did not release")
		}
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second alias mutation never entered after release")
	}
}

func TestRetrievalAliasMutationGuardBoundsExternalWork(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	if err := store.WithRetrievalAliasMutation(t.Context(), "forja_retrieval", func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("alias mutation operation has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > retrievalAliasMutationTimeout {
			t.Fatalf("alias mutation deadline remaining=%s, want (0,%s]", remaining, retrievalAliasMutationTimeout)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func retrievalGenerationFixture(collectionSuffix, version string) persistence.RetrievalGenerationConfig {
	return persistence.RetrievalGenerationConfig{
		GenerationID:    contracts.RetrievalGenerationID("fixture", version, 3, "sparse-fixture-v1"),
		CollectionAlias: "forja_retrieval", CollectionName: "forja_retrieval_" + collectionSuffix,
		EmbeddingModel: "fixture", EmbeddingVersion: version, Dimensions: 3, SparseEncoderVersion: "sparse-fixture-v1",
	}
}
