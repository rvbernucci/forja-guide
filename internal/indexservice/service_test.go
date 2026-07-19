package indexservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/knowledge"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestServicePublishesArtifactBeforeCanonicalAuthority(t *testing.T) {
	now := time.Date(2026, 7, 19, 16, 0, 0, 123456000, time.UTC)
	bundle, descriptor := serviceBundle(t, now.Add(-time.Minute))
	artifacts := &recordingArtifactPublisher{}
	repository := &recordingIndexRepository{}
	service, err := New(artifacts, repository, clock.Fixed{Time: now})
	if err != nil {
		t.Fatal(err)
	}
	metadata := runstate.CommandMetadata{
		IdempotencyKey: "publish-index-service", ActorType: "system",
		ActorID: "index-service", CorrelationID: "publish-index-service",
	}
	command := PublishCommand{
		Bundle: bundle, Metadata: metadata,
		AdapterRuns: []persistence.IndexAdapterRun{{Adapter: descriptor, Status: "passed"}},
		Deltas:      []persistence.IndexDelta{}, Invalidations: []persistence.IndexInvalidation{},
	}
	first, err := service.Publish(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Publish(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotID != second.SnapshotID || len(artifacts.commands) != 2 ||
		artifacts.commands[0].Intent.OperationID != artifacts.commands[1].Intent.OperationID {
		t.Fatal("service retry changed deterministic authority")
	}
	if repository.publications[0].Bundle.Snapshot.Status != "active" ||
		repository.publications[0].Bundle.Snapshot.ArtifactContentHash == nil ||
		*repository.publications[0].Bundle.Snapshot.ValidatedAt != now {
		t.Fatalf("publication=%#v", repository.publications[0].Bundle.Snapshot)
	}
	if strings.Contains(string(artifacts.bodies[0]), "artifact_content_hash") {
		t.Fatal("immutable artifact body contains its own authority hash")
	}
}

type recordingArtifactPublisher struct {
	commands []knowledge.PublishArtifactCommand
	bodies   [][]byte
}

func (r *recordingArtifactPublisher) PublishArtifact(_ context.Context, command knowledge.PublishArtifactCommand) (contracts.Artifact, error) {
	body, err := io.ReadAll(command.Body)
	if err != nil {
		return contracts.Artifact{}, err
	}
	digest := sha256.Sum256(body)
	if command.Intent.ContentHash != "sha256:"+hex.EncodeToString(digest[:]) || command.Intent.SizeBytes != int64(len(body)) {
		return contracts.Artifact{}, io.ErrUnexpectedEOF
	}
	r.commands = append(r.commands, command)
	r.bodies = append(r.bodies, body)
	return contracts.Artifact{
		ArtifactID: command.Intent.ArtifactID, ContentHash: command.Intent.ContentHash,
		Kind: command.Intent.Kind, Status: "active",
	}, nil
}

type recordingIndexRepository struct {
	publications []persistence.IndexPublication
}

func (r *recordingIndexRepository) PublishIndexSnapshot(_ context.Context, value persistence.IndexPublication, _ runstate.CommandMetadata) (contracts.RepositorySnapshot, error) {
	r.publications = append(r.publications, value)
	return value.Bundle.Snapshot, nil
}

func (r *recordingIndexRepository) GetActiveIndexSnapshot(context.Context) (contracts.RepositorySnapshot, bool, error) {
	if len(r.publications) == 0 {
		return contracts.RepositorySnapshot{}, false, nil
	}
	return r.publications[len(r.publications)-1].Bundle.Snapshot, true, nil
}

func serviceBundle(t *testing.T, createdAt time.Time) (indexing.IndexBundle, contracts.AdapterDescriptor) {
	t.Helper()
	descriptor := contracts.AdapterDescriptor{
		Name: "python", Version: "3.14",
		ConfigurationHash: serviceHash("adapter-config"), CapabilityHash: serviceHash("adapter-capability"),
	}
	adapterSetHash, err := contracts.ComputeAdapterSetHash([]contracts.AdapterDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := contracts.RepositorySnapshot{
		SchemaVersion: contracts.IndexSchemaVersion,
		TenantID:      "tenant_00000000-0000-4000-8000-000000000001",
		RepositoryID:  "repo_00000000-0000-4000-8000-000000000002",
		SourceCommit:  strings.Repeat("a", 40), SourceTree: strings.Repeat("b", 40),
		ConfigurationHash: serviceHash("snapshot-config"), AdapterSetHash: adapterSetHash,
		Adapters: []contracts.AdapterDescriptor{descriptor}, Status: "proposed", Version: 1,
		Counts: contracts.SnapshotCounts{Files: 1}, CreatedBy: "index-service", CreatedAt: createdAt,
	}
	snapshot.SnapshotID = contracts.ComputeSnapshotID(snapshot)
	file := contracts.FileCard{
		SchemaVersion: contracts.IndexSchemaVersion, SnapshotID: snapshot.SnapshotID,
		RepositoryID: snapshot.RepositoryID, SourceCommit: snapshot.SourceCommit,
		Path: "app.py", GitBlobID: strings.Repeat("c", 40), SourceHash: serviceHash("body"),
		SizeBytes: 8, Language: "python", SymbolIDs: []string{}, Diagnostics: []contracts.DiagnosticSummary{},
	}
	file.FileID = contracts.ComputeFileID(file)
	file.LineageID = contracts.ComputeFileLineageID(file)
	return indexing.IndexBundle{
		Snapshot: snapshot, Files: []contracts.FileCard{file},
		Symbols: []contracts.SymbolCard{}, Relations: []contracts.RelationEvidence{},
	}, descriptor
}

func serviceHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
