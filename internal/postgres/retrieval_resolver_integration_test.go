package postgres

import (
	"strings"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestResolveRetrievalPointRequiresActiveCanonicalSymbolAndHash(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	publication := indexPublicationFixture(t, pool, "resolver", strings.Repeat("a", 40))
	if _, err := store.PublishIndexSnapshot(t.Context(), publication, testMetadata("retrieval-resolver-index")); err != nil {
		t.Fatal(err)
	}
	file := publication.Bundle.Files[0]
	symbol := publication.Bundle.Symbols[0]
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, "sparse-fixture-v1")
	pointID := contracts.RetrievalPointID(generation, symbol.SymbolID, file.SourceHash)
	sourceHash, err := decodeContentHash(file.SourceHash)
	if err != nil {
		t.Fatal(err)
	}
	cardHash, err := decodeContentHash(indexHash("retrieval-card"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_generations (
			tenant_id, repository_id, generation_id, collection_alias, collection_name,
			embedding_model, embedding_version, dimensions, sparse_encoder_version, status
		) VALUES ($1,$2,$3,'retrieval','retrieval_fixture','fixture','v1',3,'sparse-fixture-v1','active')`,
		DefaultTenantID, DefaultRepositoryID, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_projection_points (
			tenant_id, repository_id, generation_id, point_id, entity_id, artifact_family,
			source_commit, source_sha256, card_sha256, status, authority_class,
			stale, language, symbol_kind, repository_path, proof_refs
		) VALUES ($1,$2,$3,$4,$5,'symbol',$6,$7,$8,'active','canonical',false,'python','function',$9,'["snapshot"]')`,
		DefaultTenantID, DefaultRepositoryID, generation, pointID, symbol.SymbolID,
		publication.Bundle.Snapshot.SourceCommit, sourceHash, cardHash, file.Path); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveRetrievalPoint(t.Context(), pointID)
	if err != nil || len(resolved) != 1 || resolved[0].EntityID != symbol.SymbolID || resolved[0].SourceHash != file.SourceHash {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	wrongHash := indexHash("wrong-source")
	wrongPoint := contracts.RetrievalPointID(generation, symbol.SymbolID, wrongHash)
	wrongBytes, err := decodeContentHash(wrongHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_projection_points (
			tenant_id, repository_id, generation_id, point_id, entity_id, artifact_family,
			source_commit, source_sha256, card_sha256, status, authority_class, stale, proof_refs
		) VALUES ($1,$2,$3,$4,$5,'symbol',$6,$7,$8,'active','canonical',false,'[]')`,
		DefaultTenantID, DefaultRepositoryID, generation, wrongPoint, symbol.SymbolID,
		publication.Bundle.Snapshot.SourceCommit, wrongBytes, cardHash); err != nil {
		t.Fatal(err)
	}
	if resolved, err := store.ResolveRetrievalPoint(t.Context(), wrongPoint); err != nil || len(resolved) != 0 {
		t.Fatalf("hash-mismatched resolved=%#v err=%v", resolved, err)
	}
}
