package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

type recordingQdrantUpserter struct {
	request *qdrant.UpsertPoints
	err     error
}

func (client *recordingQdrantUpserter) Upsert(_ context.Context, request *qdrant.UpsertPoints) (*qdrant.UpdateResult, error) {
	client.request = request
	return &qdrant.UpdateResult{}, client.err
}

func TestQdrantCollectionPlanIsStrictAndIndexed(t *testing.T) {
	plan, err := BuildQdrantCollectionPlan(
		"forja_retrieval_v1",
		3,
		contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion),
	)
	if err != nil {
		t.Fatalf("BuildQdrantCollectionPlan() error = %v", err)
	}
	if !plan.Create.GetStrictModeConfig().GetEnabled() || plan.Create.GetStrictModeConfig().GetUnindexedFilteringRetrieve() {
		t.Fatalf("strict mode=%#v", plan.Create.GetStrictModeConfig())
	}
	seen := map[string]bool{}
	for _, index := range plan.PayloadIndex {
		seen[index.FieldName] = true
	}
	for _, required := range []string{"tenant_id", "repository_id", "source_commit", "status", "stale", "path_scope"} {
		if !seen[required] {
			t.Fatalf("missing required payload index %q", required)
		}
	}
}

func TestQdrantPointPreservesCanonicalPointIdentity(t *testing.T) {
	point, err := BuildPoint(
		context.Background(), validCardSource(),
		contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion),
		fixtureEmbedder{}, HashingSparseEncoder{},
	)
	if err != nil {
		t.Fatalf("BuildPoint() error = %v", err)
	}
	wire, err := QdrantPoint(point)
	if err != nil {
		t.Fatalf("QdrantPoint() error = %v", err)
	}
	if wire.GetId().GetUuid() == "" || wire.GetPayload()["point_id"].GetStringValue() != point.PointID {
		t.Fatalf("wire identity does not preserve canonical point: %#v", wire)
	}
	if got := wire.GetPayload()["path_scope"].GetListValue().GetValues(); len(got) < 2 || got[0].GetStringValue() != "**" {
		t.Fatalf("path scope=%#v", got)
	}
}

func TestBuildQdrantQueryRequestAppliesMandatoryFiltersToBothRanks(t *testing.T) {
	query := contracts.RetrievalQuery{
		RequestID: "retrieval_request_test", SchemaVersion: contracts.RetrievalSchemaVersion,
		TenantID: "tenant_10000000-0000-4000-8000-000000000001", RepositoryID: "repo_10000000-0000-4000-8000-000000000002",
		Query: "find LoginHandler", Scope: contracts.RetrievalScope{SourceCommit: strings.Repeat("a", 40), AllowedPaths: []string{"internal/**"}, DeniedPaths: []string{"internal/private/**"}},
		Filters: contracts.RetrievalFilters{ArtifactFamilies: []string{"symbol"}, AuthorityClasses: []string{"canonical"}},
		Policy:  contracts.RetrievalPolicy{Limit: 2, DenseLimit: 3, SparseLimit: 4, DenseWeight: 1, SparseWeight: 1, RRFK: 60},
	}
	for _, mode := range []string{DenseVectorName, SparseVectorName} {
		request, err := BuildQdrantQueryRequest("forja_retrieval_v1", query, []float64{0.1, 0.2, 0.3}, contracts.SparseVector{Indices: []uint32{2, 8}, Values: []float64{0.4, 0.6}}, mode)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if request.GetFilter() == nil || request.GetUsing() != mode || request.GetWithPayload() == nil {
			t.Fatalf("mode %s request=%#v", mode, request)
		}
		if !hasKeywordCondition(request.GetFilter().Must, "tenant_id", query.TenantID) || !hasKeywordCondition(request.GetFilter().Must, "source_commit", query.Scope.SourceCommit) || !hasBoolCondition(request.GetFilter().Must, "stale", false) {
			t.Fatalf("mode %s missing mandatory filter: %#v", mode, request.GetFilter())
		}
	}
}

func TestNormalizeScopePathsAndUUIDAreStable(t *testing.T) {
	if got := normalizeScopePaths([]string{"src/**", "**", "src/**"}); strings.Join(got, ",") != "**,src" {
		t.Fatalf("scope=%v", got)
	}
	first := pointUUID("retrieval_" + strings.Repeat("a", 64))
	if first != pointUUID("retrieval_"+strings.Repeat("a", 64)) || len(first) != 36 || first[14] != '4' {
		t.Fatalf("uuid=%q", first)
	}
}

func TestQdrantEndpointRequiresTLSAndSecretBeyondLoopback(t *testing.T) {
	if _, err := (QdrantEndpoint{Host: "qdrant.internal", Port: 6334}).ClientConfig(); err == nil {
		t.Fatal("plaintext non-loopback endpoint accepted")
	}
	config, err := (QdrantEndpoint{Host: "127.0.0.1", Port: 6334}).ClientConfig()
	if err != nil || config.UseTLS || config.PoolSize != 1 {
		t.Fatalf("loopback config=%#v err=%v", config, err)
	}
	config, err = (QdrantEndpoint{Host: "qdrant.internal", Port: 6334, UseTLS: true, APIKey: "secret"}).ClientConfig()
	if err != nil || !config.UseTLS || config.APIKey != "secret" {
		t.Fatalf("TLS config=%#v err=%v", config, err)
	}
}

func TestQdrantPointWriterWaitsForIdempotentAcknowledgement(t *testing.T) {
	point, err := BuildPoint(
		context.Background(), validCardSource(),
		contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion),
		fixtureEmbedder{}, HashingSparseEncoder{},
	)
	if err != nil {
		t.Fatal(err)
	}
	client := &recordingQdrantUpserter{}
	writer := QdrantPointWriter{Client: client, CollectionName: "forja_retrieval_v1"}
	if err := writer.UpsertPoint(context.Background(), point); err != nil {
		t.Fatal(err)
	}
	if client.request == nil || !client.request.GetWait() || len(client.request.GetPoints()) != 1 || client.request.GetPoints()[0].GetPayload()["point_id"].GetStringValue() != point.PointID {
		t.Fatalf("upsert request=%#v", client.request)
	}
	client.err = errors.New("unavailable")
	if err := writer.UpsertPoint(context.Background(), point); err == nil {
		t.Fatal("Qdrant error was accepted")
	}
}

func hasKeywordCondition(conditions []*qdrant.Condition, field, value string) bool {
	for _, condition := range conditions {
		if condition.GetField().GetKey() == field && condition.GetField().GetMatch().GetKeyword() == value {
			return true
		}
	}
	return false
}

func hasBoolCondition(conditions []*qdrant.Condition, field string, value bool) bool {
	for _, condition := range conditions {
		if condition.GetField().GetKey() == field && condition.GetField().GetMatch().GetBoolean() == value {
			return true
		}
	}
	return false
}
