package retrieval

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const MemoryBodyPolicyVersion = "memory-body-redaction-v1"

var memorySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(authorization\s*:\s*bearer\s+)[a-z0-9._~+/-]{8,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
	regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
}

// PrepareMemoryBody is the deterministic content boundary used after an
// authorized object read and before any embedding request.
func PrepareMemoryBody(mediaType string, body []byte) (string, error) {
	if mediaType != "text/plain" && mediaType != "text/markdown" {
		return "", fmt.Errorf("memory body media type is not approved")
	}
	if len(body) == 0 || len(body) > MaxCardTextBytes {
		return "", fmt.Errorf("memory body is outside the retrieval limit")
	}
	text := string(body)
	for index, pattern := range memorySecretPatterns {
		if index == 0 {
			text = pattern.ReplaceAllString(text, "${1}[REDACTED]")
			continue
		}
		text = pattern.ReplaceAllString(text, "[REDACTED]")
	}
	text = strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
	if text == "" || len(text) > MaxCardTextBytes {
		return "", fmt.Errorf("memory body is empty after normalization")
	}
	return text, nil
}
