package retrieval

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// This test is opt-in because it creates and deletes physical Qdrant
// collections. It exercises the pinned official client against a real server;
// unit tests remain the default for contributors without Qdrant available.
func TestLiveQdrantBlueGreenQueryAndDelete(t *testing.T) {
	client := liveQdrantClient(t)
	guard := &recordingAliasMutationGuard{}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	blueCollection := "forja_live_blue_" + suffix
	greenCollection := "forja_live_green_" + suffix
	alias := "forja_live_alias_" + suffix
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = client.UpdateAliases(cleanupContext, []*qdrant.AliasOperations{qdrant.NewAliasDelete(alias)})
		_ = client.DeleteCollection(cleanupContext, blueCollection)
		_ = client.DeleteCollection(cleanupContext, greenCollection)
	})

	blueGeneration := contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion)
	bluePlan, err := BuildQdrantCollectionPlan(blueCollection, 3, blueGeneration)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := CutoverQdrantCollection(ctx, guard, client, alias, bluePlan)
	if err != nil || previous.Exists {
		t.Fatalf("blue cutover previous=%#v err=%v", previous, err)
	}
	greenGeneration := contracts.RetrievalGenerationID("fixture", "green-v1", 3, SparseEncoderVersion)
	greenPlan, err := BuildQdrantCollectionPlan(greenCollection, 3, greenGeneration)
	if err != nil {
		t.Fatal(err)
	}
	previous, err = CutoverQdrantCollection(ctx, guard, client, alias, greenPlan)
	if err != nil || !previous.Exists || previous.CollectionName != blueCollection {
		t.Fatalf("green cutover previous=%#v err=%v", previous, err)
	}
	if err := RollbackQdrantCollection(ctx, guard, client, alias, greenCollection, blueCollection); err != nil {
		t.Fatal(err)
	}

	source := validCardSource()
	point, err := BuildPoint(ctx, source, blueGeneration, fixtureEmbedder{}, HashingSparseEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	writer := QdrantPointWriter{Client: client, CollectionName: blueCollection}
	if err := writer.UpsertPoint(ctx, point); err != nil {
		t.Fatal(err)
	}
	query := governedQuery()
	query.ExpectedGeneration = &blueGeneration
	canonical := CanonicalCandidate{
		PointID: point.PointID, CollectionGeneration: point.CollectionGeneration,
		TenantID: point.TenantID, RepositoryID: point.RepositoryID, EntityID: point.EntityID,
		ArtifactFamily: point.ArtifactFamily, SourceCommit: point.SourceCommit, SourceHash: point.SourceHash,
		Status: point.Status, AuthorityClass: point.AuthorityClass, RepositoryPath: point.RepositoryPath,
		ProofRefs: point.ProofRefs,
	}
	service := QueryService{
		Client: client, CollectionName: blueCollection, Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{},
		Resolver: staticResolver{point.PointID: {canonical}}, QueryTimeout: 10 * time.Second,
	}
	result, err := service.Search(ctx, query)
	if err != nil || result.Status != "complete" || len(result.Accepted) != 1 || result.Accepted[0].PointID != point.PointID {
		t.Fatalf("live result=%#v err=%v", result, err)
	}
	if err := writer.DeletePoints(ctx, []string{point.PointID}); err != nil {
		t.Fatal(err)
	}
	result, err = service.Search(ctx, query)
	if err != nil || result.Status != "complete" || len(result.Accepted) != 0 {
		t.Fatalf("deleted live result=%#v err=%v", result, err)
	}
}

func liveQdrantClient(t *testing.T) *qdrant.Client {
	t.Helper()
	if os.Getenv("FORJA_QDRANT_LIVE") != "1" {
		t.Skip("set FORJA_QDRANT_LIVE=1 to run destructive real-Qdrant integration")
	}
	port := 6334
	if value := os.Getenv("FORJA_QDRANT_GRPC_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse FORJA_QDRANT_GRPC_PORT: %v", err)
		}
		port = parsed
	}
	client, err := OpenQdrant(QdrantEndpoint{
		Host: defaultLiveQdrantHost(os.Getenv("FORJA_QDRANT_HOST")), Port: port,
		APIKey: os.Getenv("FORJA_QDRANT_API_KEY"),
	})
	if err != nil {
		t.Fatalf("open live Qdrant: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func defaultLiveQdrantHost(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	return host
}
