package indexing

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const IndexArtifactMediaType = "application/vnd.forja.index-snapshot+json"

// MarshalCanonicalBundle emits the immutable evidence body before artifact
// authority is attached to the snapshot, avoiding a circular content hash.
func MarshalCanonicalBundle(bundle IndexBundle) ([]byte, error) {
	if err := ValidateBundle(bundle); err != nil {
		return nil, err
	}
	if bundle.Snapshot.Status != "proposed" || bundle.Snapshot.ArtifactID != nil ||
		bundle.Snapshot.ArtifactContentHash != nil || bundle.Snapshot.ValidatedAt != nil {
		return nil, fmt.Errorf("artifact body must contain an unbound proposed snapshot")
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(bundle); err != nil {
		return nil, fmt.Errorf("encode canonical index artifact: %w", err)
	}
	return output.Bytes(), nil
}
