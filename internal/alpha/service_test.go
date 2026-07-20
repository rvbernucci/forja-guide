package alpha

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStartResearchIsExplicitWhenRuntimeIsUnavailable(t *testing.T) {
	service := NewService(Config{Accelerator: "Not detected", SoftwareStack: "Not detected"})
	service.newID = func() (string, error) { return "research_test", nil }
	service.now = func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }

	session, err := service.StartResearch("Estimate NVIDIA's sensitivity to the 10-year real yield.")
	if err != nil {
		t.Fatal(err)
	}
	if session.State != "awaiting_local_runtime" || !strings.Contains(session.Notice, "No financial answer has been simulated") {
		t.Fatalf("unavailable runtime was obscured: %#v", session)
	}
	if len(session.Plan) != 6 || session.Plan[3].ID != "factors" {
		t.Fatalf("factor plan was not selected: %#v", session.Plan)
	}
}

func TestStartResearchRecognizesConfiguredLocalRuntime(t *testing.T) {
	endpoint, err := url.Parse("http://127.0.0.1:8000/v1")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(Config{ModelBaseURL: endpoint, EmbeddingBaseURL: endpoint})
	service.newID = func() (string, error) { return "research_test", nil }

	session, err := service.StartResearch("Compare the latest filings.")
	if err != nil {
		t.Fatal(err)
	}
	if session.State != "planned" {
		t.Fatalf("configured runtime state = %q", session.State)
	}
	if _, ok := service.Research(session.ID); !ok {
		t.Fatal("research session was not retained")
	}
}

func TestStartResearchRejectsInvalidPrompt(t *testing.T) {
	service := NewService(Config{})
	if _, err := service.StartResearch("  "); err == nil {
		t.Fatal("blank prompt was accepted")
	}
	if _, err := service.StartResearch(strings.Repeat("x", 4001)); err == nil {
		t.Fatal("oversized prompt was accepted")
	}
}
