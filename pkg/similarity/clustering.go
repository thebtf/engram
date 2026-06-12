// Package similarity provides text similarity and clustering utilities for
// deduplicating and grouping semantically related observations.
package similarity

import (
	"math/bits"
	"strings"

	"github.com/thebtf/engram/pkg/models"
)

// stopWords is the set of common English function words excluded from term
// extraction. Defined at package level so it is allocated once rather than
// rebuilt on every addTerms call.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "must": true, "shall": true,
	"this": true, "that": true, "these": true, "those": true,
	"and": true, "or": true, "but": true, "if": true, "then": true,
	"for": true, "from": true, "with": true, "about": true, "into": true,
	"to": true, "of": true, "in": true, "on": true, "at": true, "by": true,
	"it": true, "its": true, "which": true, "who": true, "what": true,
	"when": true, "where": true, "how": true, "why": true,
}

// ClusterObservations groups similar observations and returns one representative
// per cluster. Callers should pass observations sorted by preference (e.g.
// descending recency) because the first observation encountered in each cluster
// is kept as the representative.
//
// similarityThreshold is a Jaccard value in [0,1]. Observations whose Jaccard
// similarity meets or exceeds the threshold are placed in the same cluster.
// The spec amendment C13 documents the canonical caller value of 0.7.
//
// Algorithm selection:
//   - n ≤ 50: simple O(n²) pairwise comparison — no overhead for small sets.
//   - n > 50: optimized path with a 64-bit term-signature pre-filter to skip
//     obviously-distant pairs before the full Jaccard computation.
func ClusterObservations(observations []*models.Observation, similarityThreshold float64) []*models.Observation {
	if len(observations) <= 1 {
		return observations
	}

	if len(observations) <= 50 {
		return clusterObservationsSimple(observations, similarityThreshold)
	}

	return clusterObservationsOptimized(observations, similarityThreshold)
}

// clusterObservationsSimple is the O(n²) reference implementation used for
// sets of 50 or fewer observations where the quadratic cost is negligible.
func clusterObservationsSimple(observations []*models.Observation, similarityThreshold float64) []*models.Observation {
	// Pre-compute term sets so each observation is tokenized once.
	termSets := make([]map[string]bool, len(observations))
	for i, obs := range observations {
		termSets[i] = ExtractObservationTerms(obs)
	}

	clustered := make([]bool, len(observations))
	result := make([]*models.Observation, 0)

	for i := 0; i < len(observations); i++ {
		if clustered[i] {
			continue
		}

		// First occurrence in a cluster becomes its representative.
		// Because callers sort by recency, this retains the newest observation.
		result = append(result, observations[i])
		clustered[i] = true

		for j := i + 1; j < len(observations); j++ {
			if clustered[j] {
				continue
			}
			if JaccardSimilarity(termSets[i], termSets[j]) >= similarityThreshold {
				clustered[j] = true
			}
		}
	}

	return result
}

// observationEntry bundles a term set with its 64-bit signature so both are
// computed once per observation in the optimized path.
type observationEntry struct {
	terms     map[string]bool
	signature uint64
}

// clusterObservationsOptimized uses a 64-bit term-signature pre-filter to avoid
// running the full O(|terms|) Jaccard comparison for pairs that are unlikely to
// be similar. This reduces average complexity from O(n²) toward O(n·k) where k
// is the fraction of pairs that survive the signature gate.
//
// The pre-filter rejects pairs whose signatures differ in more than 32 bits
// (i.e. popcount(sigA XOR sigB) > 32). Signatures are XOR-folded FNV-1a hashes
// of the term strings, so this is a probabilistic lower bound on overlap — pairs
// that pass the gate are still verified with exact Jaccard.
func clusterObservationsOptimized(observations []*models.Observation, similarityThreshold float64) []*models.Observation {
	n := len(observations)

	entries := make([]observationEntry, n)
	for i, obs := range observations {
		terms := ExtractObservationTerms(obs)
		entries[i] = observationEntry{
			terms:     terms,
			signature: computeTermSignature(terms),
		}
	}

	clustered := make([]bool, n)
	// Pre-allocate assuming roughly half of observations are unique duplicates.
	result := make([]*models.Observation, 0, n/2)

	for i := 0; i < n; i++ {
		if clustered[i] {
			continue
		}

		result = append(result, observations[i])
		clustered[i] = true

		sigI := entries[i].signature
		termsI := entries[i].terms

		for j := i + 1; j < n; j++ {
			if clustered[j] {
				continue
			}

			// Signature gate: if more than half the bits differ, the term sets
			// are likely too far apart to exceed the similarity threshold. Skip
			// the more expensive Jaccard calculation for these pairs.
			if popCount64(sigI^entries[j].signature) > 32 {
				continue
			}

			if JaccardSimilarity(termsI, entries[j].terms) >= similarityThreshold {
				clustered[j] = true
			}
		}
	}

	return result
}

// computeTermSignature produces a 64-bit fingerprint for a term set by
// XOR-folding per-term FNV-1a hashes. XOR makes the result order-independent
// (set semantics). The signature is used only for fast pre-filtering, not as a
// precise similarity metric.
func computeTermSignature(terms map[string]bool) uint64 {
	const (
		fnvOffset uint64 = 14695981039346656037
		fnvPrime  uint64 = 1099511628211
	)

	var sig uint64
	for term := range terms {
		h := fnvOffset
		for i := 0; i < len(term); i++ {
			h ^= uint64(term[i])
			h *= fnvPrime
		}
		// XOR-fold into the running signature so the result is set-order-independent.
		sig ^= h
	}
	return sig
}

// popCount64 counts the number of set bits in a 64-bit integer.
// Delegates to bits.OnesCount64, which the Go compiler lowers to a hardware
// POPCNT instruction on supported architectures.
func popCount64(x uint64) int {
	return bits.OnesCount64(x)
}

// IsSimilarToAny reports whether newObs has Jaccard similarity ≥
// similarityThreshold with any observation in existing. Returns false
// immediately when existing is empty or newObs has no extractable terms.
func IsSimilarToAny(newObs *models.Observation, existing []*models.Observation, similarityThreshold float64) bool {
	if len(existing) == 0 {
		return false
	}

	newTerms := ExtractObservationTerms(newObs)
	if len(newTerms) == 0 {
		return false
	}

	for _, obs := range existing {
		existingTerms := ExtractObservationTerms(obs)
		if JaccardSimilarity(newTerms, existingTerms) >= similarityThreshold {
			return true
		}
	}

	return false
}

// ExtractObservationTerms builds the term set used for Jaccard similarity from
// an observation's title, narrative, facts, and file paths. File paths are
// reduced to their basename component to avoid penalizing identical files under
// different working directories.
func ExtractObservationTerms(obs *models.Observation) map[string]bool {
	terms := make(map[string]bool)

	addTerms(terms, obs.Title.String)
	addTerms(terms, obs.Narrative.String)

	for _, fact := range obs.Facts {
		addTerms(terms, fact)
	}

	// Reduce file paths to basenames. Full paths differ across workstations but
	// the filename alone is a meaningful signal for content similarity.
	for _, file := range obs.FilesRead {
		parts := strings.Split(file, "/")
		if len(parts) > 0 {
			terms[strings.ToLower(parts[len(parts)-1])] = true
		}
	}
	for _, file := range obs.FilesModified {
		parts := strings.Split(file, "/")
		if len(parts) > 0 {
			terms[strings.ToLower(parts[len(parts)-1])] = true
		}
	}

	return terms
}

// addTerms tokenizes text and adds meaningful terms to terms. Tokenization
// splits on anything that is not [a-z0-9_], lowercases all tokens, and discards
// tokens shorter than 3 characters or matching a stop word.
func addTerms(terms map[string]bool, text string) {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_')
	})

	for _, word := range words {
		if len(word) >= 3 && !stopWords[word] {
			terms[word] = true
		}
	}
}

// JaccardSimilarity returns the Jaccard index of two term sets: the ratio of
// intersection size to union size, in [0, 1]. Two empty sets are defined as
// identical (returns 1.0); one empty set returns 0.0.
func JaccardSimilarity(set1, set2 map[string]bool) float64 {
	if len(set1) == 0 && len(set2) == 0 {
		return 1.0
	}
	if len(set1) == 0 || len(set2) == 0 {
		return 0.0
	}

	intersection := 0
	for term := range set1 {
		if set2[term] {
			intersection++
		}
	}

	union := len(set1) + len(set2) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}
