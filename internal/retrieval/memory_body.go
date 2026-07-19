package retrieval

import (
	"fmt"
	"strings"
	"unicode"
)

const MemoryBodyPolicyVersion = "memory-body-redaction-v1"

// PrepareMemoryBody is the deterministic content boundary used after an
// authorized object read and before any embedding request.
func PrepareMemoryBody(mediaType string, body []byte) (string, error) {
	if mediaType != "text/plain" && mediaType != "text/markdown" {
		return "", fmt.Errorf("memory body media type is not approved")
	}
	if len(body) == 0 || len(body) > MaxCardTextBytes {
		return "", fmt.Errorf("memory body is outside the retrieval limit")
	}
	text := strings.Join(strings.FieldsFunc(string(body), unicode.IsSpace), " ")
	if text == "" || len(text) > MaxCardTextBytes {
		return "", fmt.Errorf("memory body is empty after normalization")
	}
	return text, nil
}
