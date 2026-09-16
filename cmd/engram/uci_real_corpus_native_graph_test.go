package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

const (
	uciRealCorpusNativeGraphSourcePath       = "plugin/openclaw-engram/src/index.ts"
	uciRealCorpusNativeGraphTargetPath       = "plugin/openclaw-engram/src/client.ts"
	uciRealCorpusNativeGraphTargetSymbol     = "class:EngramRestClient"
	uciRealCorpusNativeGraphTypeScript       = "typescript"
	uciRealCorpusNativeGraphTSX              = "tsx"
	uciRealCorpusNativeGraphBuildSymbol      = "function:build"
	uciRealCorpusNativeGraphWidgetSymbol     = "function:Widget"
	uciRealCorpusNativeGraphScreenSymbol     = "function:Screen"
	uciRealCorpusNativeGraphCallerPath       = uciRealCorpusTSRoot + "/caller.ts"
	uciRealCorpusNativeGraphBridgePath       = uciRealCorpusTSRoot + "/bridge.ts"
	uciRealCorpusNativeGraphRemotePath       = uciRealCorpusTSRoot + "/remote.ts"
	uciRealCorpusNativeGraphScreenPath       = uciRealCorpusTSRoot + "/screen.tsx"
	uciRealCorpusNativeGraphWidgetPath       = uciRealCorpusTSRoot + "/widget.tsx"
	uciRealCorpusNativeGraphResolverRevision = "uci-prepared-tree-sitter-module/v1"
	uciRealCorpusNativeGraphRuleKey          = "tree-sitter-module-alias/v1"
)

type uciRealCorpusNativeGraph struct {
	Import                                       uciRealCorpusNativeGraphEdge `json:"import"`
	ImportAlias                                  uciRealCorpusNativeGraphEdge `json:"import_alias"`
	Reexport                                     uciRealCorpusNativeGraphEdge `json:"reexport"`
	ReverseDependency                            uciRealCorpusNativeGraphEdge `json:"reverse_dependency"`
	CallerLocalBuildAlias                        uciRealCorpusNativeGraphEdge `json:"caller_local_build_alias"`
	BridgePublicBuildReexport                    uciRealCorpusNativeGraphEdge `json:"bridge_public_build_reexport"`
	ScreenRemoteWidgetAlias                      uciRealCorpusNativeGraphEdge `json:"screen_remote_widget_alias"`
	ScreenRemoteWidgetReference                  uciRealCorpusNativeGraphEdge `json:"screen_remote_widget_reference"`
	CallerLocalBuildReverseDependency            uciRealCorpusNativeGraphEdge `json:"caller_local_build_reverse_dependency"`
	ScreenRemoteWidgetReferenceReverseDependency uciRealCorpusNativeGraphEdge `json:"screen_remote_widget_reference_reverse_dependency"`
}

type uciRealCorpusNativeGraphEdge struct {
	SourcePath             string `json:"source_path"`
	TargetPath             string `json:"target_path"`
	Relation               string `json:"relation"`
	SourceSymbol           string `json:"source_symbol,omitempty"`
	TargetSymbol           string `json:"target_symbol"`
	ReferenceKind          string `json:"reference_kind"`
	ByteStart              int64  `json:"byte_start"`
	ByteEnd                int64  `json:"byte_end"`
	LineStart              int    `json:"line_start"`
	LineEnd                int    `json:"line_end"`
	SourceDigest           string `json:"source_digest"`
	TargetDigest           string `json:"target_digest"`
	TargetArtifactDigest   string `json:"target_artifact_digest"`
	TargetDefinitionDigest string `json:"target_definition_digest"`
	RawTargetDigest        string `json:"raw_target_digest"`
	LocalAliasDigest       string `json:"local_alias_digest"`
	EdgeDigest             string `json:"edge_digest"`
	EvidenceDigest         string `json:"evidence_digest"`
}

type uciRealCorpusNativeGraphExpectation struct {
	kind           string
	relation       string
	sourceLanguage string
	targetLanguage string
	sourcePath     string
	targetPath     string
	sourceSymbol   string
	targetSymbol   string
	rawTarget      string
	importedSymbol string
	localAlias     string
	referenceKey   string
}

type uciRealCorpusNativeGraphRow struct {
	EdgeKey                string `gorm:"column:edge_key"`
	SourcePath             string `gorm:"column:source_path"`
	TargetPath             string `gorm:"column:target_path"`
	SourceSymbol           string `gorm:"column:source_symbol"`
	TargetSymbol           string `gorm:"column:target_symbol"`
	Relation               string `gorm:"column:relation"`
	EvidenceKind           string `gorm:"column:evidence_kind"`
	ResolutionState        string `gorm:"column:resolution_state"`
	ResolverRevision       string `gorm:"column:resolver_revision"`
	TargetArtifactID       string `gorm:"column:target_artifact_id"`
	SourceContentDigest    string `gorm:"column:source_content_digest"`
	TargetContentDigest    string `gorm:"column:target_content_digest"`
	SourceBody             []byte `gorm:"column:source_body"`
	ReferenceSiteID        string `gorm:"column:reference_site_id"`
	ReferenceSiteKey       string `gorm:"column:reference_site_key"`
	ReferenceRelation      string `gorm:"column:reference_relation"`
	ReferenceRawTarget     string `gorm:"column:reference_raw_target"`
	ReferenceSyntaxSpan    string `gorm:"column:reference_syntax_span"`
	ReferenceResolverHints string `gorm:"column:reference_resolver_hints"`
	EvidenceJSON           string `gorm:"column:evidence_json"`
	TargetDefinitionID     string `gorm:"column:target_definition_id"`
}

type uciRealCorpusNativeGraphSpan struct {
	ByteStart int64 `json:"byte_start"`
	ByteEnd   int64 `json:"byte_end"`
	LineStart int   `json:"line_start"`
	LineEnd   int   `json:"line_end"`
}

type uciRealCorpusNativeGraphEvidenceSpan struct {
	ByteStart int64 `json:"ByteStart"`
	ByteEnd   int64 `json:"ByteEnd"`
	LineStart int   `json:"LineStart"`
	LineEnd   int   `json:"LineEnd"`
}

type uciRealCorpusNativeGraphEvidence struct {
	ReferenceSiteID *string                              `json:"ReferenceSiteID"`
	Span            uciRealCorpusNativeGraphEvidenceSpan `json:"Span"`
	RuleKey         string                               `json:"RuleKey"`
	Explanation     string                               `json:"Explanation"`
}

type uciRealCorpusNativeGraphReferenceHints struct {
	Kind      string `json:"kind"`
	SymbolKey string `json:"symbol_key"`
}

func uciVerifyRealCorpusNativeGraph(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication) (uciRealCorpusNativeGraph, error) {
	if err := uciRealCorpusNativeGraphValidateAuthority(ctx, authority, publication); err != nil {
		return uciRealCorpusNativeGraph{}, err
	}

	importExpectation := uciRealCorpusNativeGraphExpectation{
		kind: "import", relation: "imports", sourceLanguage: uciRealCorpusNativeGraphTypeScript, targetLanguage: uciRealCorpusNativeGraphTypeScript, sourcePath: uciRealCorpusNativeGraphSourcePath, targetPath: uciRealCorpusNativeGraphTargetPath,
	}
	importAliasExpectation := uciRealCorpusNativeGraphExpectation{
		kind: "import_alias", relation: "imports", sourceLanguage: uciRealCorpusNativeGraphTypeScript, targetLanguage: uciRealCorpusNativeGraphTypeScript, sourcePath: uciRealCorpusNativeGraphSourcePath, targetPath: uciRealCorpusNativeGraphTargetPath, targetSymbol: uciRealCorpusNativeGraphTargetSymbol, rawTarget: "EngramRestClient",
	}
	reexportExpectation := uciRealCorpusNativeGraphExpectation{
		kind: "reexport_alias", relation: "exports", sourceLanguage: uciRealCorpusNativeGraphTypeScript, targetLanguage: uciRealCorpusNativeGraphTypeScript, sourcePath: uciRealCorpusNativeGraphSourcePath, targetPath: uciRealCorpusNativeGraphTargetPath, targetSymbol: uciRealCorpusNativeGraphTargetSymbol, rawTarget: "EngramRestClient",
	}
	callerLocalBuildExpectation := uciRealCorpusNativeGraphExpectation{
		kind: "import_alias", relation: "imports", sourceLanguage: uciRealCorpusNativeGraphTypeScript, targetLanguage: uciRealCorpusNativeGraphTypeScript, sourcePath: uciRealCorpusNativeGraphCallerPath, targetPath: uciRealCorpusNativeGraphRemotePath, targetSymbol: uciRealCorpusNativeGraphBuildSymbol, rawTarget: "build as localBuild", importedSymbol: "build", localAlias: "localBuild", referenceKey: "import:./remote.ts#build:localBuild",
	}
	bridgePublicBuildExpectation := uciRealCorpusNativeGraphExpectation{
		kind: "reexport_alias", relation: "exports", sourceLanguage: uciRealCorpusNativeGraphTypeScript, targetLanguage: uciRealCorpusNativeGraphTypeScript, sourcePath: uciRealCorpusNativeGraphBridgePath, targetPath: uciRealCorpusNativeGraphRemotePath, targetSymbol: uciRealCorpusNativeGraphBuildSymbol, rawTarget: "build as publicBuild", importedSymbol: "build", localAlias: "publicBuild", referenceKey: "reexport:./remote.ts#build:publicBuild",
	}
	screenRemoteWidgetExpectation := uciRealCorpusNativeGraphExpectation{
		kind: "import_alias", relation: "imports", sourceLanguage: uciRealCorpusNativeGraphTSX, targetLanguage: uciRealCorpusNativeGraphTSX, sourcePath: uciRealCorpusNativeGraphScreenPath, targetPath: uciRealCorpusNativeGraphWidgetPath, targetSymbol: uciRealCorpusNativeGraphWidgetSymbol, rawTarget: "Widget as RemoteWidget", importedSymbol: "Widget", localAlias: "RemoteWidget", referenceKey: "import:./widget.tsx#Widget:RemoteWidget",
	}
	screenRemoteWidgetReferenceExpectation := uciRealCorpusNativeGraphExpectation{
		kind: "jsx_reference", relation: "references", sourceLanguage: uciRealCorpusNativeGraphTSX, targetLanguage: uciRealCorpusNativeGraphTSX, sourcePath: uciRealCorpusNativeGraphScreenPath, targetPath: uciRealCorpusNativeGraphWidgetPath, sourceSymbol: uciRealCorpusNativeGraphScreenSymbol, targetSymbol: uciRealCorpusNativeGraphWidgetSymbol, rawTarget: "RemoteWidget",
	}

	importRow, err := uciRealCorpusNativeGraphExactEdge(ctx, authority, publication, importExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	importAliasRow, err := uciRealCorpusNativeGraphExactEdge(ctx, authority, publication, importAliasExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	reexportRow, err := uciRealCorpusNativeGraphExactEdge(ctx, authority, publication, reexportExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	callerLocalBuildRow, err := uciRealCorpusNativeGraphExactEdge(ctx, authority, publication, callerLocalBuildExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	bridgePublicBuildRow, err := uciRealCorpusNativeGraphExactEdge(ctx, authority, publication, bridgePublicBuildExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	screenRemoteWidgetRow, err := uciRealCorpusNativeGraphExactEdge(ctx, authority, publication, screenRemoteWidgetExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	screenRemoteWidgetReferenceRow, err := uciRealCorpusNativeGraphExactEdge(ctx, authority, publication, screenRemoteWidgetReferenceExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	if err := uciRealCorpusNativeGraphIncomingEdge(ctx, authority, publication, importAliasExpectation, importAliasRow.EdgeKey); err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	if err := uciRealCorpusNativeGraphIncomingEdge(ctx, authority, publication, callerLocalBuildExpectation, callerLocalBuildRow.EdgeKey); err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	if err := uciRealCorpusNativeGraphIncomingEdge(ctx, authority, publication, screenRemoteWidgetReferenceExpectation, screenRemoteWidgetReferenceRow.EdgeKey); err != nil {
		return uciRealCorpusNativeGraph{}, err
	}

	importProof, err := uciRealCorpusNativeGraphProof(importRow, importExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	importAliasProof, err := uciRealCorpusNativeGraphProof(importAliasRow, importAliasExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	reexportProof, err := uciRealCorpusNativeGraphProof(reexportRow, reexportExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	callerLocalBuildProof, err := uciRealCorpusNativeGraphProof(callerLocalBuildRow, callerLocalBuildExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	bridgePublicBuildProof, err := uciRealCorpusNativeGraphProof(bridgePublicBuildRow, bridgePublicBuildExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	screenRemoteWidgetProof, err := uciRealCorpusNativeGraphProof(screenRemoteWidgetRow, screenRemoteWidgetExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	screenRemoteWidgetReferenceProof, err := uciRealCorpusNativeGraphProof(screenRemoteWidgetReferenceRow, screenRemoteWidgetReferenceExpectation)
	if err != nil {
		return uciRealCorpusNativeGraph{}, err
	}
	return uciRealCorpusNativeGraph{
		Import:                            importProof,
		ImportAlias:                       importAliasProof,
		Reexport:                          reexportProof,
		ReverseDependency:                 importAliasProof,
		CallerLocalBuildAlias:             callerLocalBuildProof,
		BridgePublicBuildReexport:         bridgePublicBuildProof,
		ScreenRemoteWidgetAlias:           screenRemoteWidgetProof,
		ScreenRemoteWidgetReference:       screenRemoteWidgetReferenceProof,
		CallerLocalBuildReverseDependency: callerLocalBuildProof,
		ScreenRemoteWidgetReferenceReverseDependency: screenRemoteWidgetReferenceProof,
	}, nil
}

func uciRealCorpusNativeGraphValidateAuthority(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication) error {
	if ctx == nil {
		return errors.New("real-corpus native graph context is required")
	}
	if authority == nil || authority.store == nil || authority.source == nil || authority.profile == nil {
		return errors.New("real-corpus native graph authority is incomplete")
	}
	if publication.sourceID == "" || publication.checkoutID == "" || publication.viewID == "" || publication.profileID == "" || publication.generation < 1 {
		return errors.New("real-corpus native graph publication is incomplete")
	}
	checkout := authority.checkouts[uciInstalledAcceptanceClientA]
	if checkout == nil || authority.source.SourceID != publication.sourceID || authority.profile.ProfileID != publication.profileID || checkout.CheckoutID != publication.checkoutID {
		return errors.New("real-corpus native graph publication is outside the installed authority")
	}
	return nil
}

func uciRealCorpusNativeGraphExactEdge(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, expected uciRealCorpusNativeGraphExpectation) (uciRealCorpusNativeGraphRow, error) {
	var rows []uciRealCorpusNativeGraphRow
	err := authority.store.GetDB().WithContext(ctx).Raw(uciRealCorpusNativeGraphExactEdgeSQL,
		publication.viewID,
		publication.sourceID,
		publication.checkoutID,
		publication.profileID,
		publication.generation,
		expected.sourceLanguage,
		expected.targetLanguage,
		expected.sourcePath,
		expected.targetPath,
		expected.relation,
		expected.sourceSymbol,
		expected.targetSymbol,
	).Scan(&rows).Error
	if err != nil {
		return uciRealCorpusNativeGraphRow{}, fmt.Errorf("query real-corpus native graph edge: %w", err)
	}
	if len(rows) != 1 {
		return uciRealCorpusNativeGraphRow{}, errors.New("real-corpus native graph supported edge is missing or ambiguous")
	}
	if err := uciRealCorpusNativeGraphValidateRow(rows[0], expected); err != nil {
		return uciRealCorpusNativeGraphRow{}, err
	}
	return rows[0], nil
}

func uciRealCorpusNativeGraphIncomingEdge(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, expected uciRealCorpusNativeGraphExpectation, edgeKey string) error {
	type incomingRow struct {
		EdgeKey string `gorm:"column:edge_key"`
	}
	var rows []incomingRow
	err := authority.store.GetDB().WithContext(ctx).Raw(uciRealCorpusNativeGraphIncomingEdgeSQL,
		publication.viewID,
		publication.sourceID,
		publication.checkoutID,
		publication.profileID,
		publication.generation,
		expected.targetPath,
		expected.sourcePath,
		expected.targetPath,
		expected.relation,
		expected.sourceSymbol,
		expected.targetSymbol,
	).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("query real-corpus native graph incoming edge: %w", err)
	}
	if len(rows) != 1 || rows[0].EdgeKey != edgeKey {
		return errors.New("real-corpus native graph incoming reverse edge is missing or ambiguous")
	}
	return nil
}

func uciRealCorpusNativeGraphValidateRow(row uciRealCorpusNativeGraphRow, expected uciRealCorpusNativeGraphExpectation) error {
	if err := uciRealCorpusNativeGraphValidateExpectedPaths(expected); err != nil {
		return err
	}
	if err := uciRealCorpusNativeGraphValidateRowIdentity(row, expected); err != nil {
		return err
	}
	return uciRealCorpusNativeGraphValidateRowEvidence(row, expected)
}

func uciRealCorpusNativeGraphValidateExpectedPaths(expected uciRealCorpusNativeGraphExpectation) error {
	if expected.sourcePath == "" || expected.targetPath == "" || expected.sourcePath == expected.targetPath {
		return errors.New("real-corpus native graph expected source and target paths must be distinct")
	}
	if expected.sourceLanguage == "" || expected.targetLanguage == "" {
		return errors.New("real-corpus native graph expected languages are incomplete")
	}
	return nil
}

func uciRealCorpusNativeGraphValidateRowIdentity(row uciRealCorpusNativeGraphRow, expected uciRealCorpusNativeGraphExpectation) error {
	if row.SourcePath != expected.sourcePath || row.TargetPath != expected.targetPath || row.Relation != expected.relation || row.SourceSymbol != expected.sourceSymbol || row.TargetSymbol != expected.targetSymbol {
		return errors.New("real-corpus native graph edge does not match the expected source, target, relation, or symbols")
	}
	if row.EvidenceKind != "resolved" || row.ResolutionState != "resolved" || row.ResolverRevision != uciRealCorpusNativeGraphResolverRevision || row.ReferenceRelation != expected.relation {
		return errors.New("real-corpus native graph edge lacks supported resolved evidence")
	}
	if expected.targetSymbol != "" && row.TargetDefinitionID == "" {
		return errors.New("real-corpus native graph edge target symbol is not present in the selected View")
	}
	if row.TargetArtifactID == "" || row.SourceContentDigest == "" || row.TargetContentDigest == "" || row.EdgeKey == "" || row.ReferenceSiteID == "" || row.ReferenceSiteKey == "" || row.ReferenceRawTarget == "" || len(row.SourceBody) == 0 {
		return errors.New("real-corpus native graph edge source, target, or reference evidence is incomplete")
	}
	return nil
}

func uciRealCorpusNativeGraphValidateRowEvidence(row uciRealCorpusNativeGraphRow, expected uciRealCorpusNativeGraphExpectation) error {
	referenceSpan, evidence, hints, err := uciRealCorpusNativeGraphDecodeRowEvidence(row)
	if err != nil {
		return err
	}
	if evidence.ReferenceSiteID == nil || *evidence.ReferenceSiteID != row.ReferenceSiteID || evidence.RuleKey != uciRealCorpusNativeGraphRuleKey || evidence.Explanation == "" || hints.Kind != expected.kind || hints.SymbolKey == "" {
		return errors.New("real-corpus native graph edge is not bound to the expected source reference")
	}
	if expected.rawTarget != "" && row.ReferenceRawTarget != expected.rawTarget {
		return errors.New("real-corpus native graph edge source reference does not match the expected symbol")
	}
	if expected.localAlias != "" {
		if err := uciRealCorpusNativeGraphValidateAlias(row, expected, referenceSpan, hints); err != nil {
			return err
		}
	}
	if !uciRealCorpusNativeGraphEqualSpans(referenceSpan, evidence.Span) {
		return errors.New("real-corpus native graph edge and source reference span differ")
	}
	return uciRealCorpusNativeGraphValidateSpan(row.SourceBody, row.ReferenceRawTarget, referenceSpan)
}

func uciRealCorpusNativeGraphDecodeRowEvidence(row uciRealCorpusNativeGraphRow) (uciRealCorpusNativeGraphSpan, uciRealCorpusNativeGraphEvidence, uciRealCorpusNativeGraphReferenceHints, error) {
	var referenceSpan uciRealCorpusNativeGraphSpan
	if err := json.Unmarshal([]byte(row.ReferenceSyntaxSpan), &referenceSpan); err != nil {
		return uciRealCorpusNativeGraphSpan{}, uciRealCorpusNativeGraphEvidence{}, uciRealCorpusNativeGraphReferenceHints{}, fmt.Errorf("decode real-corpus native graph reference span: %w", err)
	}
	var evidence uciRealCorpusNativeGraphEvidence
	if err := json.Unmarshal([]byte(row.EvidenceJSON), &evidence); err != nil {
		return uciRealCorpusNativeGraphSpan{}, uciRealCorpusNativeGraphEvidence{}, uciRealCorpusNativeGraphReferenceHints{}, fmt.Errorf("decode real-corpus native graph edge evidence: %w", err)
	}
	var hints uciRealCorpusNativeGraphReferenceHints
	if err := json.Unmarshal([]byte(row.ReferenceResolverHints), &hints); err != nil {
		return uciRealCorpusNativeGraphSpan{}, uciRealCorpusNativeGraphEvidence{}, uciRealCorpusNativeGraphReferenceHints{}, fmt.Errorf("decode real-corpus native graph reference hints: %w", err)
	}
	return referenceSpan, evidence, hints, nil
}

func uciRealCorpusNativeGraphValidateAlias(row uciRealCorpusNativeGraphRow, expected uciRealCorpusNativeGraphExpectation, referenceSpan uciRealCorpusNativeGraphSpan, hints uciRealCorpusNativeGraphReferenceHints) error {
	if expected.importedSymbol == "" || expected.referenceKey == "" || expected.rawTarget != expected.importedSymbol+" as "+expected.localAlias || expected.targetSymbol != "function:"+expected.importedSymbol {
		return errors.New("real-corpus native graph alias expectation is inconsistent")
	}
	expectedReferenceSiteKey := fmt.Sprintf("%s@%d:%d", expected.referenceKey, referenceSpan.ByteStart, referenceSpan.ByteEnd)
	if row.ReferenceSiteKey != expectedReferenceSiteKey || hints.SymbolKey != expected.sourceLanguage+":"+expectedReferenceSiteKey {
		return errors.New("real-corpus native graph alias reference does not prove the expected renamed binding")
	}
	return nil
}

func uciRealCorpusNativeGraphProof(row uciRealCorpusNativeGraphRow, expected uciRealCorpusNativeGraphExpectation) (uciRealCorpusNativeGraphEdge, error) {
	if row.TargetArtifactID == "" || (expected.targetSymbol != "" && row.TargetDefinitionID == "") {
		return uciRealCorpusNativeGraphEdge{}, errors.New("real-corpus native graph proof is missing target identity evidence")
	}
	var span uciRealCorpusNativeGraphSpan
	if err := json.Unmarshal([]byte(row.ReferenceSyntaxSpan), &span); err != nil {
		return uciRealCorpusNativeGraphEdge{}, fmt.Errorf("decode real-corpus native graph proof span: %w", err)
	}
	var hints uciRealCorpusNativeGraphReferenceHints
	if err := json.Unmarshal([]byte(row.ReferenceResolverHints), &hints); err != nil {
		return uciRealCorpusNativeGraphEdge{}, fmt.Errorf("decode real-corpus native graph proof hints: %w", err)
	}
	if hints.Kind == "" {
		return uciRealCorpusNativeGraphEdge{}, errors.New("real-corpus native graph proof is missing reference kind")
	}
	targetDefinitionDigest := ""
	if row.TargetDefinitionID != "" {
		targetDefinitionDigest = uciInstalledAcceptanceStringDigest(row.TargetDefinitionID)
	}
	localAliasDigest := ""
	if expected.localAlias != "" {
		localAliasDigest = uciInstalledAcceptanceStringDigest(expected.localAlias)
	}
	return uciRealCorpusNativeGraphEdge{
		SourcePath:             row.SourcePath,
		TargetPath:             row.TargetPath,
		Relation:               row.Relation,
		SourceSymbol:           row.SourceSymbol,
		TargetSymbol:           row.TargetSymbol,
		ReferenceKind:          hints.Kind,
		ByteStart:              span.ByteStart,
		ByteEnd:                span.ByteEnd,
		LineStart:              span.LineStart,
		LineEnd:                span.LineEnd,
		SourceDigest:           row.SourceContentDigest,
		TargetDigest:           row.TargetContentDigest,
		TargetArtifactDigest:   uciInstalledAcceptanceStringDigest(row.TargetArtifactID),
		TargetDefinitionDigest: targetDefinitionDigest,
		RawTargetDigest:        uciInstalledAcceptanceStringDigest(row.ReferenceRawTarget),
		LocalAliasDigest:       localAliasDigest,
		EdgeDigest:             uciInstalledAcceptanceStringDigest(row.EdgeKey),
		EvidenceDigest:         uciInstalledAcceptanceStringDigest(row.ReferenceSiteID + "\x00" + row.ReferenceSiteKey + "\x00" + row.EvidenceJSON),
	}, nil
}

func uciRealCorpusNativeGraphEqualSpans(reference uciRealCorpusNativeGraphSpan, evidence uciRealCorpusNativeGraphEvidenceSpan) bool {
	return reference.ByteStart == evidence.ByteStart &&
		reference.ByteEnd == evidence.ByteEnd &&
		reference.LineStart == evidence.LineStart &&
		reference.LineEnd == evidence.LineEnd
}

func uciRealCorpusNativeGraphValidateSpan(source []byte, rawTarget string, span uciRealCorpusNativeGraphSpan) error {
	if rawTarget == "" || span.ByteStart < 0 || span.ByteEnd <= span.ByteStart || span.ByteEnd > int64(len(source)) || span.LineStart < 1 || span.LineEnd < span.LineStart {
		return errors.New("real-corpus native graph source span is invalid")
	}
	if string(source[span.ByteStart:span.ByteEnd]) != rawTarget {
		return errors.New("real-corpus native graph source span does not match persisted source")
	}
	lineEndOffset := span.ByteEnd - 1
	if uciRealCorpusNativeGraphLineForOffset(source, span.ByteStart) != span.LineStart || uciRealCorpusNativeGraphLineForOffset(source, lineEndOffset) != span.LineEnd {
		return errors.New("real-corpus native graph source span line coordinates do not match persisted source")
	}
	return nil
}

func uciRealCorpusNativeGraphLineForOffset(source []byte, offset int64) int {
	line := 1
	for index := int64(0); index < offset; index++ {
		if source[index] == '\n' {
			line++
		}
	}
	return line
}

const uciRealCorpusNativeGraphExactEdgeSQL = `
WITH selected_view AS (
	SELECT
		view_row.source_id,
		view_row.checkout_id,
		view_row.generation,
		profile.parser_bundle_digest
	FROM ci_views AS view_row
	JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
	WHERE view_row.view_id = ?
		AND view_row.source_id = ?
		AND view_row.checkout_id = ?
		AND view_row.profile_id = ?
		AND view_row.generation = ?
		AND view_row.state IN ('published', 'superseded')
)
SELECT
	edge.edge_key,
	edge.source_path,
	edge.target_path,
	COALESCE(edge.source_symbol, '') AS source_symbol,
	COALESCE(edge.target_symbol, '') AS target_symbol,
	edge.relation,
	edge.evidence_kind,
	edge.resolution_state,
	edge.resolver_revision,
	edge.target_artifact::text AS target_artifact_id,
	source_blob.content_digest AS source_content_digest,
	target_blob.content_digest AS target_content_digest,
	source_blob.safe_content AS source_body,
	reference.reference_site_id::text AS reference_site_id,
	reference.site_key AS reference_site_key,
	reference.relation AS reference_relation,
	reference.raw_target AS reference_raw_target,
	reference.syntax_span::text AS reference_syntax_span,
	reference.resolver_hints::text AS reference_resolver_hints,
	edge.evidence_json::text AS evidence_json,
	COALESCE(target_definition.definition_id::text, '') AS target_definition_id
FROM selected_view AS view_row
JOIN ci_resolved_edges AS edge
	ON edge.source_id = view_row.source_id
	AND edge.checkout_id = view_row.checkout_id
	AND edge.valid_from_generation <= view_row.generation
	AND (edge.valid_to_generation IS NULL OR edge.valid_to_generation > view_row.generation)
JOIN ci_memberships AS source_membership
	ON source_membership.source_id = view_row.source_id
	AND source_membership.checkout_id = view_row.checkout_id
	AND source_membership.path_key = edge.source_path
	AND source_membership.artifact_id = edge.source_artifact
	AND source_membership.valid_from_generation <= view_row.generation
	AND (source_membership.valid_to_generation IS NULL OR source_membership.valid_to_generation > view_row.generation)
	AND source_membership.file_state = 'present'
JOIN ci_parse_artifacts AS source_artifact
	ON source_artifact.source_id = view_row.source_id
	AND source_artifact.artifact_id = edge.source_artifact
	AND source_artifact.extraction_profile_digest = view_row.parser_bundle_digest
	AND source_artifact.language = ?
	AND source_artifact.status IN ('complete', 'partial')
	AND source_artifact.sealed_at IS NOT NULL
	AND source_artifact.facts_digest IS NOT NULL
JOIN ci_blobs AS source_blob
	ON source_blob.source_id = source_artifact.source_id
	AND source_blob.blob_id = source_artifact.blob_id
	AND source_blob.storage_state = 'stored'
	AND source_blob.safe_content IS NOT NULL
JOIN ci_reference_sites AS reference
	ON reference.artifact_id = edge.source_artifact
	AND reference.reference_site_id::text = (edge.evidence_json ->> 'ReferenceSiteID')
JOIN ci_memberships AS target_membership
	ON target_membership.source_id = view_row.source_id
	AND target_membership.checkout_id = view_row.checkout_id
	AND target_membership.path_key = edge.target_path
	AND target_membership.artifact_id = edge.target_artifact
	AND target_membership.valid_from_generation <= view_row.generation
	AND (target_membership.valid_to_generation IS NULL OR target_membership.valid_to_generation > view_row.generation)
	AND target_membership.file_state = 'present'
JOIN ci_parse_artifacts AS target_artifact
	ON target_artifact.source_id = view_row.source_id
	AND target_artifact.artifact_id = target_membership.artifact_id
	AND target_artifact.extraction_profile_digest = view_row.parser_bundle_digest
	AND target_artifact.language = ?
	AND target_artifact.status IN ('complete', 'partial')
	AND target_artifact.sealed_at IS NOT NULL
	AND target_artifact.facts_digest IS NOT NULL
JOIN ci_blobs AS target_blob
	ON target_blob.source_id = target_artifact.source_id
	AND target_blob.blob_id = target_artifact.blob_id
	AND target_blob.storage_state = 'stored'
	AND target_blob.safe_content IS NOT NULL
LEFT JOIN ci_definitions AS target_definition
	ON target_definition.artifact_id = target_artifact.artifact_id
	AND target_definition.local_symbol_key = edge.target_symbol
WHERE edge.source_path = ?
	AND edge.target_path = ?
	AND edge.source_path <> edge.target_path
	AND edge.relation = ?
	AND edge.evidence_kind = 'resolved'
	AND edge.resolution_state = 'resolved'
	AND edge.resolver_revision = 'uci-prepared-tree-sitter-module/v1'
	AND reference.relation = edge.relation
	AND COALESCE(edge.source_symbol, '') = ?
	AND COALESCE(edge.target_symbol, '') = ?
ORDER BY edge.edge_key ASC
`

const uciRealCorpusNativeGraphIncomingEdgeSQL = `
WITH selected_view AS (
	SELECT
		view_row.source_id,
		view_row.checkout_id,
		view_row.generation,
		profile.parser_bundle_digest
	FROM ci_views AS view_row
	JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
	WHERE view_row.view_id = ?
		AND view_row.source_id = ?
		AND view_row.checkout_id = ?
		AND view_row.profile_id = ?
		AND view_row.generation = ?
		AND view_row.state IN ('published', 'superseded')
), incoming_target AS (
	SELECT membership.artifact_id
	FROM selected_view AS view_row
	JOIN ci_memberships AS membership
		ON membership.source_id = view_row.source_id
		AND membership.checkout_id = view_row.checkout_id
		AND membership.valid_from_generation <= view_row.generation
		AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
		AND membership.file_state = 'present'
	WHERE membership.path_key = ?
)
SELECT edge.edge_key
FROM incoming_target AS target
JOIN ci_resolved_edges AS edge
	ON edge.target_artifact = target.artifact_id
JOIN selected_view AS view_row
	ON edge.source_id = view_row.source_id
	AND edge.checkout_id = view_row.checkout_id
	AND edge.valid_from_generation <= view_row.generation
	AND (edge.valid_to_generation IS NULL OR edge.valid_to_generation > view_row.generation)
JOIN ci_memberships AS source_membership
	ON source_membership.source_id = view_row.source_id
	AND source_membership.checkout_id = view_row.checkout_id
	AND source_membership.path_key = edge.source_path
	AND source_membership.artifact_id = edge.source_artifact
	AND source_membership.valid_from_generation <= view_row.generation
	AND (source_membership.valid_to_generation IS NULL OR source_membership.valid_to_generation > view_row.generation)
	AND source_membership.file_state = 'present'
JOIN ci_reference_sites AS reference
	ON reference.artifact_id = edge.source_artifact
	AND reference.reference_site_id::text = (edge.evidence_json ->> 'ReferenceSiteID')
WHERE edge.source_path = ?
	AND edge.target_path = ?
	AND edge.source_path <> edge.target_path
	AND edge.relation = ?
	AND edge.evidence_kind = 'resolved'
	AND edge.resolution_state = 'resolved'
	AND edge.resolver_revision = 'uci-prepared-tree-sitter-module/v1'
	AND reference.relation = edge.relation
	AND COALESCE(edge.source_symbol, '') = ?
	AND COALESCE(edge.target_symbol, '') = ?
ORDER BY edge.edge_key ASC
`

func TestUCIRealCorpusNativeGraphSpanMatchesPersistedBytes(t *testing.T) {
	source := []byte("const π = 1;\nimport { EngramRestClient } from './client.js';\n")
	rawTarget := "EngramRestClient"
	span := uciRealCorpusNativeGraphSpan{
		ByteStart: int64(len("const π = 1;\nimport { ")),
		ByteEnd:   int64(len("const π = 1;\nimport { ") + len(rawTarget)),
		LineStart: 2,
		LineEnd:   2,
	}
	if err := uciRealCorpusNativeGraphValidateSpan(source, rawTarget, span); err != nil {
		t.Fatal(err)
	}

	span.ByteEnd--
	if err := uciRealCorpusNativeGraphValidateSpan(source, rawTarget, span); err == nil {
		t.Fatal("source span with a truncated byte range was accepted")
	}

	span.ByteEnd++
	span.LineStart = 1
	span.LineEnd = 1
	if err := uciRealCorpusNativeGraphValidateSpan(source, rawTarget, span); err == nil {
		t.Fatal("source span with incorrect line coordinates was accepted")
	}
}
