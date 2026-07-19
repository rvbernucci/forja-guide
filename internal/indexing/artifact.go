package indexing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const IndexArtifactMediaType = "application/vnd.forja.index-snapshot+json"

// MarshalCanonicalBundle emits the immutable evidence body before artifact
// authority is attached to the snapshot, avoiding a circular content hash.
func MarshalCanonicalBundle(bundle IndexBundle) ([]byte, error) {
	canonical, err := CanonicalizeBundle(bundle)
	if err != nil {
		return nil, err
	}
	if canonical.Snapshot.Status != "proposed" || canonical.Snapshot.ArtifactID != nil ||
		canonical.Snapshot.ArtifactContentHash != nil || canonical.Snapshot.ValidatedAt != nil {
		return nil, fmt.Errorf("artifact body must contain an unbound proposed snapshot")
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(canonical); err != nil {
		return nil, fmt.Errorf("encode canonical index artifact: %w", err)
	}
	return output.Bytes(), nil
}

func CanonicalizeBundle(bundle IndexBundle) (IndexBundle, error) {
	if err := ValidateBundle(bundle); err != nil {
		return IndexBundle{}, err
	}
	canonical := bundle
	canonical.Files = make([]contracts.FileCard, len(bundle.Files))
	canonical.Symbols = make([]contracts.SymbolCard, len(bundle.Symbols))
	canonical.Relations = make([]contracts.RelationEvidence, len(bundle.Relations))
	copy(canonical.Files, bundle.Files)
	copy(canonical.Symbols, bundle.Symbols)
	copy(canonical.Relations, bundle.Relations)
	sort.Slice(canonical.Files, func(i, j int) bool {
		return canonical.Files[i].FileID < canonical.Files[j].FileID
	})
	sort.Slice(canonical.Symbols, func(i, j int) bool {
		return canonical.Symbols[i].SymbolID < canonical.Symbols[j].SymbolID
	})
	sort.Slice(canonical.Relations, func(i, j int) bool {
		return canonical.Relations[i].RelationID < canonical.Relations[j].RelationID
	})
	return canonical, nil
}
