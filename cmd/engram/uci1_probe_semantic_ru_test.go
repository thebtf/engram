package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/thebtf/engram/internal/uci"
)

const (
	uci1SemanticRUQuery = "Какая функция вызывает другую функцию и возвращает её результат?"

	uci1SemanticRUMissingProviderSeam = "U16 omitted: installed server requires caller-supplied ENGRAM_EMBEDDING_URL and ENGRAM_EMBEDDING_MODEL at launch; restart the installed server before rerunning the U16 probe"
)

type uci1SemanticRUEmbeddingProfile struct {
	EmbeddingProfileID    string `gorm:"column:embedding_profile_id"`
	ProviderRef           string `gorm:"column:provider_ref"`
	Model                 string `gorm:"column:model"`
	Dimension             int    `gorm:"column:dimension"`
	PreprocessingRevision string `gorm:"column:preprocessing_revision"`
	IncludeRelativePath   bool   `gorm:"column:include_relative_path"`
}

func uci1ProbeSemanticRUInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
	providerURL, model, configured := uci1SemanticRUProviderEnvironment(runtime.ServerEnvironment)
	if !configured {
		return nil, errors.New(uci1SemanticRUMissingProviderSeam)
	}
	if uci1SemanticRUHasLexicalOverlap(uci1SemanticRUQuery, runtime.Request.Fixture.PrimarySource, runtime.Request.Fixture.LinkedSource) {
		return nil, errors.New("U16 Russian semantic query has lexical overlap with the English fixture code")
	}
	if ctx == nil || runtime.ClientA == nil || runtime.Authority == nil || runtime.Authority.store == nil {
		return nil, errors.New("U16 installed semantic probe runtime is incomplete")
	}

	selection, selected := runtime.Selections[uciInstalledAcceptanceClientA]
	publication, published := runtime.Publications[uciInstalledAcceptanceClientA]
	if !selected || !published || selection.contextHandle == "" || selection.runID == "" {
		return nil, errors.New("U16 installed semantic probe has no selected current View")
	}

	status, err := uciInstalledAcceptanceStatusForSelection(ctx, runtime.ClientA, selection)
	if err != nil {
		return nil, fmt.Errorf("observe U16 installed current View: %w", err)
	}
	current, err := uciInstalledAcceptanceStatusPublication(status, selection)
	if err != nil {
		return nil, fmt.Errorf("bind U16 installed current View: %w", err)
	}
	if !uci1SemanticRUPublicationMatches(current, publication) {
		return nil, errors.New("U16 installed current View differs from the published baseline")
	}

	payload, err := runtime.ClientA.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          uci1SemanticRUQuery,
		"path_prefix":    "pkg",
		"limit":          5,
	})
	if err != nil {
		return nil, fmt.Errorf("run U16 Russian semantic search through installed client: %w", err)
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return nil, fmt.Errorf("decode U16 Russian semantic search: %w", err)
	}
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) || !uciInstalledAcceptanceQueryMatchesPublication(response, current) || response.Retrieval == nil || response.Retrieval.Mode != uci.QueryRetrievalHybrid || response.Retrieval.VectorCoverage == nil || *response.Retrieval.VectorCoverage < 1 || response.Items == nil {
		mode := uci.QueryRetrievalMode("")
		coverage := -1.0
		if response.Retrieval != nil {
			mode = response.Retrieval.Mode
			if response.Retrieval.VectorCoverage != nil {
				coverage = *response.Retrieval.VectorCoverage
			}
		}
		structural := uci.IndexCoverageState("")
		unresolved, unsupported := int64(-1), int64(-1)
		if response.Coverage != nil {
			structural = response.Coverage.Structural
			if response.Coverage.UnresolvedSites != nil {
				unresolved = *response.Coverage.UnresolvedSites
			}
			if response.Coverage.UnsupportedFiles != nil {
				unsupported = *response.Coverage.UnsupportedFiles
			}
		}
		return nil, fmt.Errorf("U16 Russian semantic search incomplete: status=%s current_view=%t retrieval=%s vector_coverage=%g structural=%s unresolved=%d unsupported=%d items=%t", response.Status, uciInstalledAcceptanceQueryMatchesPublication(response, current), mode, coverage, structural, unresolved, unsupported, response.Items != nil)
	}

	var citation *uci.QueryItem
	for index := range *response.Items {
		item := &(*response.Items)[index]
		if item.Ref.SourceID != current.sourceID || item.Ref.ViewID != current.viewID {
			return nil, errors.New("U16 Russian semantic search disclosed an item outside the current View")
		}
		name, isGoFunction := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path != runtime.Request.Fixture.RelativePath || !isGoFunction || name != runtime.Request.Fixture.SharedSymbol {
			continue
		}
		if !uciInstalledAcceptanceIsBareSHA256(string(item.ContentDigest)) {
			return nil, errors.New("U16 Russian semantic search citation has an invalid digest")
		}
		if len(item.MatchSources) != 1 || item.MatchSources[0] != uci.QueryMatchVector {
			return nil, errors.New("U16 Russian semantic search did not prove vector-only retrieval")
		}
		if citation != nil {
			return nil, errors.New("U16 Russian semantic search returned multiple target citations")
		}
		citation = item
	}
	if citation == nil {
		return nil, errors.New("U16 Russian semantic search omitted the delegated-call fixture citation")
	}

	readPayload, err := runtime.ClientA.Tool(ctx, "codebase_read", map[string]any{
		"context_handle": selection.contextHandle,
		"ref": map[string]any{
			"source_id":  citation.Ref.SourceID,
			"view_id":    citation.Ref.ViewID,
			"entity_key": citation.Ref.EntityKey,
		},
		"span": map[string]any{
			"byte_start": citation.Span.ByteStart,
			"byte_end":   citation.Span.ByteEnd,
			"line_start": citation.Span.LineStart,
			"line_end":   citation.Span.LineEnd,
		},
		"content_digest":      string(citation.ContentDigest),
		"verify_working_copy": false,
		"max_bytes":           8_192,
	})
	if err != nil {
		return nil, fmt.Errorf("read U16 Russian semantic citation through installed client: %w", err)
	}
	read, err := uciDecodeInstalledAcceptanceQuery(readPayload)
	if err != nil {
		return nil, fmt.Errorf("decode U16 Russian citation readback: %w", err)
	}
	if read.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(read, current) || read.Items == nil || len(*read.Items) != 1 {
		return nil, errors.New("U16 Russian semantic citation readback is not a one-item current-View result")
	}
	readItem := (*read.Items)[0]
	if readItem.Ref != citation.Ref || readItem.Span != citation.Span || readItem.ContentDigest != citation.ContentDigest {
		return nil, errors.New("U16 Russian semantic citation did not read back exactly")
	}

	profile, err := uci1SemanticRUEmbeddingProfileFor(ctx, runtime.Authority, current, providerURL, model)
	if err != nil {
		return nil, err
	}
	digest := uciInstalledAcceptanceStringDigest(strings.Join([]string{
		"U16",
		uci1SemanticRUQuery,
		profile.ProviderRef,
		profile.Model,
		profile.EmbeddingProfileID,
		profile.PreprocessingRevision,
		current.sourceID,
		current.checkoutID,
		current.profileID,
		current.viewID,
		strconv.FormatInt(current.generation, 10),
		citation.Ref.EntityKey,
		citation.Path,
		string(citation.ContentDigest),
		string(readItem.ContentDigest),
	}, "\n"))

	return map[string]uciInstalledAcceptanceScenarioEvidence{
		"U16": {
			Code:   uciInstalledAcceptanceScenarioCodeObserved,
			Digest: digest,
		},
	}, nil
}

func uci1SemanticRUProviderEnvironment(entries []string) (string, string, bool) {
	var providerURL, model string
	for _, entry := range entries {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		switch key {
		case "ENGRAM_EMBEDDING_URL":
			providerURL = value
		case "ENGRAM_EMBEDDING_MODEL":
			model = value
		}
	}
	if providerURL == "" || model == "" || strings.TrimSpace(providerURL) != providerURL || strings.TrimSpace(model) != model {
		return "", "", false
	}
	return providerURL, model, true
}

func uci1SemanticRUHasLexicalOverlap(query string, sources ...string) bool {
	queryTerms := uci1SemanticRUTerms(query)
	for _, source := range sources {
		for term := range uci1SemanticRUTerms(source) {
			if _, found := queryTerms[term]; found {
				return true
			}
		}
	}
	return false
}

func uci1SemanticRUTerms(value string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, term := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if term != "" {
			terms[term] = struct{}{}
		}
	}
	return terms
}

func uci1SemanticRUPublicationMatches(current, baseline uciInstalledAcceptancePublication) bool {
	return current.sourceID == baseline.sourceID &&
		current.checkoutID == baseline.checkoutID &&
		current.viewID == baseline.viewID &&
		current.profileID == baseline.profileID &&
		current.generation == baseline.generation &&
		current.runID == baseline.runID
}

func uci1SemanticRUEmbeddingProfileFor(ctx context.Context, authority *uciInstalledAcceptanceAuthority, current uciInstalledAcceptancePublication, providerURL, model string) (uci1SemanticRUEmbeddingProfile, error) {
	if authority == nil || authority.store == nil || current.profileID == "" {
		return uci1SemanticRUEmbeddingProfile{}, errors.New("U16 semantic profile authority is incomplete")
	}
	providerRef := "sha256:" + uciInstalledAcceptanceStringDigest(providerURL)
	var profiles []uci1SemanticRUEmbeddingProfile
	result := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT embedding_profile_id, provider_ref, model, dimension, preprocessing_revision, include_relative_path
		FROM ci_embedding_profiles
		WHERE analysis_profile_id = ? AND provider_ref = ? AND model = ?
	`, current.profileID, providerRef, model).Scan(&profiles)
	if result.Error != nil {
		return uci1SemanticRUEmbeddingProfile{}, fmt.Errorf("read U16 installed semantic profile: %w", result.Error)
	}
	if len(profiles) != 1 {
		return uci1SemanticRUEmbeddingProfile{}, errors.New("U16 installed semantic profile does not bind one configured provider and model")
	}
	profile := profiles[0]
	if profile.EmbeddingProfileID == "" || profile.ProviderRef != providerRef || profile.Model != model || profile.Dimension != 1536 || profile.PreprocessingRevision == "" || !profile.IncludeRelativePath {
		return uci1SemanticRUEmbeddingProfile{}, errors.New("U16 installed semantic profile does not match the active provider contract")
	}
	return profile, nil
}
