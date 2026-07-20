package alpha

import "time"

const ContractVersion = "1.0"

type RuntimeEndpoint struct {
	Configured bool   `json:"configured"`
	Local      bool   `json:"local"`
	Status     string `json:"status"`
}

type RuntimeStatus struct {
	Mode               string          `json:"mode"`
	LocalOnly          bool            `json:"local_only"`
	CoreInferenceReady bool            `json:"core_inference_ready"`
	Model              RuntimeEndpoint `json:"model"`
	Embeddings         RuntimeEndpoint `json:"embeddings"`
	Accelerator        string          `json:"accelerator"`
	SoftwareStack      string          `json:"software_stack"`
}

type Company struct {
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	CIK    string `json:"cik"`
}

type Capability struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type Bootstrap struct {
	ContractVersion  string        `json:"contract_version"`
	Product          string        `json:"product"`
	Tagline          string        `json:"tagline"`
	Runtime          RuntimeStatus `json:"runtime"`
	Universe         []Company     `json:"universe"`
	Capabilities     []Capability  `json:"capabilities"`
	SuggestedPrompts []string      `json:"suggested_prompts"`
}

type ResearchRequest struct {
	Prompt string `json:"prompt"`
}

type ResearchStep struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Tool        string `json:"tool"`
	Status      string `json:"status"`
}

type ResearchSession struct {
	ContractVersion string         `json:"contract_version"`
	ID              string         `json:"id"`
	State           string         `json:"state"`
	CreatedAt       time.Time      `json:"created_at"`
	Plan            []ResearchStep `json:"plan"`
	Notice          string         `json:"notice"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
