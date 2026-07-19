package indexing

import (
	"bytes"
	"testing"
)

func TestCanonicalIndexArtifactIsByteStableAndUnbound(t *testing.T) {
	bundle := runProcessFixture(t, NewPythonAdapter(toolRoot(t), "3.14"), []SourceDocument{
		sourceDocument("app.py", "value = 42\n", "python"),
	})
	first, err := MarshalCanonicalBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalCanonicalBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatal("canonical artifact bytes are unstable")
	}
	active := bundle
	active.Snapshot.Status = "active"
	if _, err := MarshalCanonicalBundle(active); err == nil {
		t.Fatal("artifact encoder accepted a lifecycle-bound snapshot")
	}
}
