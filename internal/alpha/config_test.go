package alpha

import "testing"

func TestLoadConfigDefaultsToPrivateLocalRuntime(t *testing.T) {
	config, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.Address != defaultAddress || config.ModelBaseURL != nil || config.EmbeddingBaseURL != nil {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadConfigAcceptsLoopbackInferenceEndpoints(t *testing.T) {
	values := map[string]string{
		"FORJA_ALPHA_MODEL_BASE_URL":     "http://127.0.0.1:8000/v1",
		"FORJA_ALPHA_EMBEDDING_BASE_URL": "http://localhost:8081/v1",
	}
	config, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.ModelBaseURL == nil || config.EmbeddingBaseURL == nil {
		t.Fatalf("local endpoints were not retained: %#v", config)
	}
}

func TestLoadConfigRejectsRemoteCoreInference(t *testing.T) {
	_, err := loadConfig(func(key string) string {
		if key == "FORJA_ALPHA_MODEL_BASE_URL" {
			return "https://api.example.com/v1"
		}
		return ""
	})
	if err == nil {
		t.Fatal("remote model endpoint was accepted")
	}
}
