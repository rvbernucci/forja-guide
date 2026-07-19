package retrieval

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const SparseEncoderVersion = "sparse-sha256-tf-l2-v1"

// SparseEncoder makes a lexical representation reproducible across hosts.
type SparseEncoder interface {
	Encode(string) (contracts.SparseVector, error)
	Version() string
}

// HashingSparseEncoder uses stable token hashes and L2-normalized TF weights.
type HashingSparseEncoder struct{}

func (HashingSparseEncoder) Version() string { return SparseEncoderVersion }

func (HashingSparseEncoder) Encode(text string) (contracts.SparseVector, error) {
	terms := lexicalTerms(text)
	if len(terms) == 0 {
		return contracts.SparseVector{}, fmt.Errorf("sparse encoder requires lexical terms")
	}
	frequencies := make(map[uint32]float64, len(terms))
	for _, term := range terms {
		frequencies[sparseTermIndex(term)]++
	}
	indices := make([]uint32, 0, len(frequencies))
	for index := range frequencies {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(left, right int) bool { return indices[left] < indices[right] })
	values := make([]float64, len(indices))
	var sumSquares float64
	for offset, index := range indices {
		weight := 1 + math.Log(frequencies[index])
		values[offset] = weight
		sumSquares += weight * weight
	}
	normalizer := math.Sqrt(sumSquares)
	for offset := range values {
		values[offset] /= normalizer
	}
	return contracts.SparseVector{Indices: indices, Values: values}, nil
}

func sparseTermIndex(term string) uint32 {
	digest := sha256.Sum256([]byte("forja-sparse-v1\x00" + term))
	return binary.BigEndian.Uint32(digest[:4])
}

func lexicalTerms(text string) []string {
	var terms []string
	for _, raw := range strings.FieldsFunc(strings.ToLower(text), func(value rune) bool {
		return !(unicode.IsLetter(value) || unicode.IsDigit(value) || value == '_' || value == '-' || value == '.')
	}) {
		for _, token := range splitIdentifier(raw) {
			if len(token) >= 2 && len(token) <= 256 {
				terms = append(terms, token)
			}
		}
	}
	return terms
}

func splitIdentifier(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	result := make([]string, 0, len(parts)+1)
	if len(value) >= 2 {
		result = append(result, value)
	}
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
