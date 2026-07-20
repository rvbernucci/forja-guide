package alpha

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Service struct {
	config   Config
	now      func() time.Time
	newID    func() (string, error)
	mu       sync.RWMutex
	sessions map[string]ResearchSession
}

func NewService(config Config) *Service {
	return &Service{
		config:   config,
		now:      time.Now,
		newID:    researchID,
		sessions: make(map[string]ResearchSession),
	}
}

func (s *Service) Bootstrap() Bootstrap {
	runtime := RuntimeStatus{
		Mode:               "readiness",
		LocalOnly:          true,
		CoreInferenceReady: s.config.ModelBaseURL != nil && s.config.EmbeddingBaseURL != nil,
		Model:              endpointStatus(s.config.ModelBaseURL != nil),
		Embeddings:         endpointStatus(s.config.EmbeddingBaseURL != nil),
		Accelerator:        s.config.Accelerator,
		SoftwareStack:      s.config.SoftwareStack,
	}
	if runtime.CoreInferenceReady {
		runtime.Mode = "local-runtime-configured"
	}
	return Bootstrap{
		ContractVersion: ContractVersion,
		Product:         "Forja Alpha",
		Tagline:         "Private investment research, grounded in evidence.",
		Runtime:         runtime,
		Universe:        magnificentSeven(),
		Capabilities: []Capability{
			{ID: "filings", Name: "Filings intelligence", Description: "SEC filing retrieval with point-in-time provenance.", Status: "planned"},
			{ID: "fundamentals", Name: "Fundamental analysis", Description: "Reproducible accounting metrics from canonical XBRL facts.", Status: "planned"},
			{ID: "factors", Name: "Factor sensitivity", Description: "Rolling exposure to market, rates and macro factors.", Status: "planned"},
			{ID: "holdings", Name: "Institutional holdings", Description: "Time-stamped 13F change and concentration analysis.", Status: "planned"},
		},
		SuggestedPrompts: []string{
			"Compare NVIDIA, Microsoft and Alphabet using the latest available filings.",
			"Which companies show improving operating margins and free-cash-flow conversion?",
			"Estimate historical sensitivity to the US 10-year real yield and test its stability.",
		},
	}
}

func (s *Service) StartResearch(prompt string) (ResearchSession, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ResearchSession{}, fmt.Errorf("prompt is required")
	}
	if len([]rune(prompt)) > 4000 {
		return ResearchSession{}, fmt.Errorf("prompt exceeds 4000 characters")
	}
	id, err := s.newID()
	if err != nil {
		return ResearchSession{}, fmt.Errorf("create research id: %w", err)
	}

	state := "awaiting_local_runtime"
	notice := "Connect the local ROCm model and embedding endpoints to execute this evidence plan. No financial answer has been simulated."
	if s.config.ModelBaseURL != nil && s.config.EmbeddingBaseURL != nil {
		state = "planned"
		notice = "The local runtime is configured. Execution adapters are the next gated capability."
	}
	session := ResearchSession{
		ContractVersion: ContractVersion,
		ID:              id,
		State:           state,
		CreatedAt:       s.now().UTC(),
		Plan:            researchPlan(prompt),
		Notice:          notice,
	}
	s.mu.Lock()
	s.sessions[id] = session
	s.mu.Unlock()
	return session, nil
}

func (s *Service) Research(id string) (ResearchSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	return session, ok
}

func endpointStatus(configured bool) RuntimeEndpoint {
	status := "not-configured"
	if configured {
		status = "configured-not-probed"
	}
	return RuntimeEndpoint{Configured: configured, Local: true, Status: status}
}

func researchID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "research_" + hex.EncodeToString(bytes), nil
}

func researchPlan(prompt string) []ResearchStep {
	steps := []ResearchStep{
		{ID: "scope", Name: "Scope the research question", Description: "Resolve companies, periods, metrics and required evidence without inferring missing intent.", Tool: "research.scope", Status: "queued"},
		{ID: "filings", Name: "Retrieve point-in-time filings", Description: "Select canonical SEC filings and XBRL facts available at the requested observation date.", Tool: "sec.retrieve", Status: "queued"},
		{ID: "fundamentals", Name: "Compute fundamental metrics", Description: "Recompute growth, margins, cash conversion and capital intensity from canonical facts.", Tool: "finance.fundamentals", Status: "queued"},
	}
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "rate") || strings.Contains(lower, "yield") || strings.Contains(lower, "bond") || strings.Contains(lower, "beta") || strings.Contains(lower, "sensitivity") {
		steps = append(steps, ResearchStep{ID: "factors", Name: "Estimate factor sensitivity", Description: "Run bounded rolling regressions with diagnostics, uncertainty and regime stability.", Tool: "finance.factors", Status: "queued"})
	}
	if strings.Contains(lower, "fund") || strings.Contains(lower, "13f") || strings.Contains(lower, "institution") || strings.Contains(lower, "holding") {
		steps = append(steps, ResearchStep{ID: "holdings", Name: "Inspect institutional disclosures", Description: "Compare time-stamped 13F holdings while preserving filing-delay and coverage limitations.", Tool: "sec.holdings", Status: "queued"})
	}
	steps = append(steps,
		ResearchStep{ID: "verify", Name: "Verify every material claim", Description: "Reject unsupported, stale, conflicting or statistically unstable claims.", Tool: "evidence.verify", Status: "queued"},
		ResearchStep{ID: "compose", Name: "Compose the research memo", Description: "Separate facts, calculations, inferences, counterarguments and unresolved gaps.", Tool: "research.compose", Status: "queued"},
	)
	return steps
}

func magnificentSeven() []Company {
	return []Company{
		{Ticker: "AAPL", Name: "Apple", CIK: "0000320193"},
		{Ticker: "MSFT", Name: "Microsoft", CIK: "0000789019"},
		{Ticker: "GOOGL", Name: "Alphabet", CIK: "0001652044"},
		{Ticker: "AMZN", Name: "Amazon", CIK: "0001018724"},
		{Ticker: "NVDA", Name: "NVIDIA", CIK: "0001045810"},
		{Ticker: "META", Name: "Meta Platforms", CIK: "0001326801"},
		{Ticker: "TSLA", Name: "Tesla", CIK: "0001318605"},
	}
}
