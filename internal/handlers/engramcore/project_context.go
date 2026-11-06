package engramcore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

type projectChoiceEvidence struct {
	scope         string
	marker        projectidentity.ProjectMarkerState
	catalog       string
	project       projectidentity.ExistingProjectChoice
	expires       time.Time
	requestDigest string
	requestNonce  string
	historicalID  string
}

type projectContextChoice struct {
	projectidentity.ExistingProjectChoice
	SelectionRef string `json:"selection_ref"`
	Provenance   string `json:"provenance,omitempty"`
}

func (m *Module) projectChoiceScope(caller muxcore.ProjectContext, transport, server, token string) string {
	raw, _ := json.Marshal([]string{caller.Cwd, caller.ID, m.v3ClientInstanceID, transport, m.envFor(caller, config.EnvClaudeSessionID), server, token})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func readUniqueJSON(decoder *json.Decoder, depth int) bool {
	if depth > 64 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !readUniqueJSON(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case json.Delim('['):
		for decoder.More() {
			if !readUniqueJSON(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	case json.Delim('}'), json.Delim(']'):
		return false
	}
	return true
}

func canonicalReadValue(value any) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			canonical, ok := canonicalReadValue(item)
			if !ok {
				return nil, false
			}
			v[key] = canonical
		}
	case []any:
		for i, item := range v {
			canonical, ok := canonicalReadValue(item)
			if !ok {
				return nil, false
			}
			v[i] = canonical
		}
	case json.Number:
		s := string(v)
		sign := ""
		if strings.HasPrefix(s, "-") {
			sign, s = "-", s[1:]
		}
		var exponent int64
		if i := strings.IndexAny(s, "eE"); i >= 0 {
			part := s[i+1:]
			digits := strings.TrimPrefix(strings.TrimPrefix(part, "+"), "-")
			if len(digits) > 9 {
				return nil, false
			}
			parsed, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return nil, false
			}
			exponent, s = parsed, s[:i]
		}
		fraction := 0
		if i := strings.IndexByte(s, '.'); i >= 0 {
			fraction = len(s) - i - 1
			s = s[:i] + s[i+1:]
		}
		s = strings.TrimLeft(s, "0")
		if s == "" {
			return json.Number("0"), true
		}
		trimmed := strings.TrimRight(s, "0")
		delta := int64(len(s)-len(trimmed)) - int64(fraction)
		if delta > 0 && exponent > math.MaxInt64-delta || delta < 0 && exponent < math.MinInt64-delta {
			return nil, false
		}
		return json.Number(sign + trimmed + "e" + strconv.FormatInt(exponent+delta, 10)), true
	}
	return value, true
}

func projectReadDigest(args []byte) (string, bool) {
	check := json.NewDecoder(bytes.NewReader(args))
	check.UseNumber()
	if !readUniqueJSON(check, 0) {
		return "", false
	}
	if _, err := check.Token(); err != io.EOF {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	var input map[string]any
	if decoder.Decode(&input) != nil || input == nil {
		return "", false
	}
	if _, exists := input["project"]; exists {
		return "", false
	}
	value, valid := canonicalReadValue(input)
	if !valid {
		return "", false
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), true
}

func (m *Module) conflictSafeRead(ctx context.Context, caller muxcore.ProjectContext, server, token string, args []byte) (string, error) {
	digest, valid := projectReadDigest(args)
	if !valid {
		return "", nil
	}
	transport := auditcontext.UCITransportSession(ctx)
	if !auditcontext.ValidUCITransportSession(transport) {
		return "", nil
	}
	scope := m.projectChoiceScope(caller, transport, server, token)
	clearScoped := func() {
		m.projectChoicesMu.Lock()
		delete(m.projectPendingReads, scope)
		delete(m.projectReadBindings, scope)
		m.projectChoicesMu.Unlock()
	}
	state, err := projectidentity.InspectProjectMarker(ctx, caller.Cwd)
	if err != nil {
		clearScoped()
		return "", err
	}
	if state.CacheID == "" {
		clearScoped()
		return "", nil
	}
	candidate := state.PortableID
	legacyID := ""
	if state.LegacyID != "" {
		project, resolved, resolveErr := projectidentity.ResolveHistoricalProject(ctx, server, token, state.LegacyID)
		if resolveErr != nil {
			clearScoped()
			return "", resolveErr
		}
		if !resolved {
			clearScoped()
			return "", nil
		}
		candidate, legacyID = project.ProjectID, project.ProjectID
	}
	if candidate == "" || candidate == state.CacheID {
		clearScoped()
		return "", nil
	}
	now := time.Now()
	m.projectChoicesMu.Lock()
	for key, value := range m.projectPendingReads {
		if now.After(value.expires) {
			delete(m.projectPendingReads, key)
		}
	}
	for key, value := range m.projectReadBindings {
		if now.After(value.expires) {
			delete(m.projectReadBindings, key)
		}
	}
	binding, found := m.projectReadBindings[scope]
	if found {
		delete(m.projectReadBindings, scope)
	}
	if !found || binding.marker != state || binding.historicalID != legacyID || binding.requestDigest != digest || now.After(binding.expires) {
		if m.projectPendingReads == nil {
			m.projectPendingReads = make(map[string]projectChoiceEvidence)
		}
		if _, exists := m.projectPendingReads[scope]; exists || len(m.projectPendingReads) < 4096 {
			var nonce [32]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				m.projectChoicesMu.Unlock()
				return "", err
			}
			m.projectPendingReads[scope] = projectChoiceEvidence{scope: scope, marker: state, historicalID: legacyID, requestDigest: digest, requestNonce: hex.EncodeToString(nonce[:]), expires: now.Add(5 * time.Minute)}
		}
		m.projectChoicesMu.Unlock()
		return "", nil
	}
	m.projectChoicesMu.Unlock()
	catalog, err := projectidentity.ReadExistingProjectChoices(ctx, server, token)
	if err != nil {
		return "", err
	}
	current, err := projectidentity.InspectProjectMarker(ctx, caller.Cwd)
	if err != nil {
		return "", err
	}
	if current != state {
		return "", &projectidentity.LogicalError{Code: "PROJECT_SELECTION_STALE"}
	}
	if legacyID != "" {
		project, resolved, resolveErr := projectidentity.ResolveHistoricalProject(ctx, server, token, state.LegacyID)
		if resolveErr != nil {
			return "", resolveErr
		}
		if !resolved || project.ProjectID != legacyID {
			return "", &projectidentity.LogicalError{Code: "PROJECT_SELECTION_STALE"}
		}
	}
	foundA, foundB := false, false
	for _, item := range catalog {
		foundA = foundA || item.ProjectID == candidate
		foundB = foundB || item.ProjectID == state.CacheID
	}
	if !foundA || !foundB {
		return "", &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_CONFLICT"}
	}
	for _, item := range catalog {
		if item.ProjectID == binding.project.ProjectID {
			return item.ProjectID, nil
		}
	}
	return "", &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_CONFLICT"}
}

func (m *Module) Tools() []module.ToolDef {
	return []module.ToolDef{{
		Name:        "project_context",
		Description: "Read current existing projects under the backend's authorization policy with action=choices. For a server-confirmed historical mapping or matching primary-origin mapping, action=repair needs no selection_ref; otherwise explicitly select a fresh choice using its selection_ref. Repair returns marker content and preconditions; it does not write a file, change historical membership or grant access. " + projectidentity.MarkerActionGuidance,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["choices","repair"]},"selection_ref":{"type":"string"}},"required":["action"]}`),
	}}
}

func (m *Module) HandleTool(ctx context.Context, caller muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != "project_context" {
		return nil, &module.ModuleError{Code: "tool_not_found", Message: "unknown local tool"}
	}
	var request struct {
		Action       string `json:"action"`
		SelectionRef string `json:"selection_ref"`
	}
	if len(args) > 4096 || json.Unmarshal(args, &request) != nil || (request.Action != "choices" && request.Action != "repair") {
		return nil, &module.ModuleError{Code: "PROJECT_SELECTION_INVALID", Message: "Use action=choices or action=repair with a fresh choice reference."}
	}
	serverURL, err := m.requireServerURL(caller)
	if err != nil {
		return nil, projectIdentityRefusal(err, caller.Cwd)
	}
	token := m.envFor(caller, config.EnvWorkstationToken)
	state, err := projectidentity.InspectProjectMarker(ctx, caller.Cwd)
	if err != nil {
		return nil, projectIdentityRefusal(err, caller.Cwd)
	}
	fileConflict := state.PortableID != "" && state.CacheID != "" && state.PortableID != state.CacheID
	var historical projectidentity.Project
	var confirmed bool
	if state.LegacyID != "" {
		historical, confirmed, err = projectidentity.ResolveHistoricalProject(ctx, serverURL, token, state.LegacyID)
		if err != nil {
			return nil, projectIdentityRefusal(err, caller.Cwd)
		}
	}
	legacyConfirmed := confirmed
	legacyCacheConflict := legacyConfirmed && state.CacheID != "" && historical.ProjectID != state.CacheID
	localConflict := fileConflict || legacyCacheConflict
	repositoryConfirmed := false
	var repository projectidentity.Project
	originConflict := false
	conflictingMappings := false
	if state.RepositoryLocator != "" && !localConflict {
		repository, repositoryConfirmed, err = projectidentity.ResolveRepositoryProject(ctx, serverURL, token, state.RepositoryLocator)
		if err != nil {
			var unknown *projectidentity.RepositoryCandidatesUnknownError
			if !errors.As(err, &unknown) || state.PinnedID != "" {
				return nil, projectIdentityRefusal(err, caller.Cwd)
			}
			originConflict = true
			confirmed = false
		}
		conflictingMappings = !originConflict && confirmed && repositoryConfirmed && historical.ProjectID != repository.ProjectID
		if conflictingMappings {
			confirmed = false
		} else if repositoryConfirmed {
			historical, confirmed = repository, true
		}
	}
	if localConflict {
		if fileConflict {
			historical = projectidentity.Project{ProjectID: state.PortableID}
		}
		repository = projectidentity.Project{ProjectID: state.CacheID}
		conflictingMappings = true
		confirmed = false
	}
	seedConfirmed := false
	var seedProject projectidentity.Project
	var seedTarget, seedDigest string
	if !confirmed && !conflictingMappings && !originConflict && state.PinnedID == "" {
		seedProject, seedConfirmed, err = projectidentity.ResolveApprovedProjectSeed(ctx, m.deps.StorageDir, serverURL, caller.Cwd, state)
		if err != nil {
			return nil, projectIdentityRefusal(err, caller.Cwd)
		}
		if seedConfirmed {
			seedTarget, seedDigest, err = projectidentity.RecoveryV2Precondition(caller.Cwd, state)
			if err != nil {
				return nil, projectIdentityRefusal(err, caller.Cwd)
			}
			historical, confirmed = seedProject, true
		}
	}
	catalog, err := projectidentity.ReadExistingProjectChoices(ctx, serverURL, token)
	if err != nil {
		return nil, projectIdentityRefusal(err, caller.Cwd)
	}
	if originConflict && len(catalog) == 0 {
		return nil, &module.ModuleError{Code: "PROJECT_IDENTITY_CONFLICT", Message: "The server reported an origin conflict without identifying candidates, and the current authorized catalog is empty. Preserve the marker and request access to the intended existing project in this host conversation."}
	}
	current, err := projectidentity.InspectProjectMarker(ctx, caller.Cwd)
	if err != nil {
		return nil, projectIdentityRefusal(err, caller.Cwd)
	}
	if current != state {
		return nil, projectIdentityRefusal(&projectidentity.LogicalError{Code: "PROJECT_SELECTION_STALE"}, caller.Cwd)
	}
	if seedConfirmed {
		again, found, seedErr := projectidentity.ResolveApprovedProjectSeed(ctx, m.deps.StorageDir, serverURL, caller.Cwd, current)
		_, digest, digestErr := projectidentity.RecoveryV2Precondition(caller.Cwd, current)
		if seedErr != nil || digestErr != nil || !found || again.ProjectID != seedProject.ProjectID || digest != seedDigest {
			return nil, projectIdentityRefusal(&projectidentity.LogicalError{Code: "PROJECT_SELECTION_STALE"}, caller.Cwd)
		}
	}
	for _, item := range catalog {
		if caller.Cwd != "" && strings.Contains(item.Name, caller.Cwd) {
			return nil, projectIdentityRefusal(&projectidentity.LogicalError{Code: "PROJECT_IDENTITY_INVALID"}, caller.Cwd)
		}
	}
	if conflictingMappings {
		foundLegacy, foundRepository := false, false
		for _, item := range catalog {
			foundLegacy = foundLegacy || item.ProjectID == historical.ProjectID
			foundRepository = foundRepository || item.ProjectID == repository.ProjectID
		}
		if !foundLegacy || !foundRepository {
			return nil, &module.ModuleError{Code: "PROJECT_IDENTITY_CONFLICT", Message: "Both conflicting projects must be present in the current authorized catalog. Preserve marker and cache, and request access to the missing project in this host conversation before obtaining fresh choices."}
		}
	}
	transportSession := auditcontext.UCITransportSession(ctx)
	scope := m.projectChoiceScope(caller, transportSession, serverURL, token)
	catalogBytes, _ := json.Marshal([]any{catalog, legacyConfirmed, historical.ProjectID, repositoryConfirmed, repository.ProjectID, seedConfirmed, seedDigest, originConflict})
	catalogHash := sha256.Sum256(catalogBytes)
	catalogDigest := hex.EncodeToString(catalogHash[:])
	if request.Action == "choices" || (conflictingMappings || originConflict) && request.SelectionRef == "" {
		choices := make([]projectContextChoice, 0, len(catalog))
		now := time.Now()
		m.projectChoicesMu.Lock()
		defer m.projectChoicesMu.Unlock()
		if m.projectChoices == nil {
			m.projectChoices = make(map[string]projectChoiceEvidence)
		}
		for ref, entry := range m.projectChoices {
			if now.After(entry.expires) {
				delete(m.projectChoices, ref)
			}
		}
		var pendingChoice projectChoiceEvidence
		if localConflict {
			pendingChoice = m.projectPendingReads[scope]
			if pendingChoice.marker != state || now.After(pendingChoice.expires) || legacyCacheConflict && pendingChoice.historicalID != historical.ProjectID {
				pendingChoice = projectChoiceEvidence{}
			}
		}
		if len(m.projectChoices)+len(catalog) > 4096 {
			return nil, &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: "Too many active choices; retry after they expire."}
		}
		for _, item := range catalog {
			if conflictingMappings && item.ProjectID != historical.ProjectID && item.ProjectID != repository.ProjectID {
				continue
			}
			provenance := ""
			if conflictingMappings {
				if fileConflict {
					if item.ProjectID == state.PortableID {
						provenance = "portable_marker"
					} else {
						provenance = "common_cache"
					}
				} else if legacyCacheConflict {
					if item.ProjectID == historical.ProjectID {
						provenance = "legacy_mapping"
					} else {
						provenance = "common_cache"
					}
				} else if item.ProjectID == historical.ProjectID {
					provenance = "legacy_mapping"
				} else {
					provenance = "primary_origin"
				}
			}
			if originConflict {
				provenance = "authorized_catalog"
			}
			var random [32]byte
			if _, err := rand.Read(random[:]); err != nil {
				return nil, projectIdentityRefusal(err, caller.Cwd)
			}
			ref := hex.EncodeToString(random[:])
			m.projectChoices[ref] = projectChoiceEvidence{scope: scope, marker: state, catalog: catalogDigest, project: item, requestDigest: pendingChoice.requestDigest, requestNonce: pendingChoice.requestNonce, expires: now.Add(5 * time.Minute)}
			choices = append(choices, projectContextChoice{ExistingProjectChoice: item, SelectionRef: ref, Provenance: provenance})
		}
		sessionBinding := "unavailable"
		if auditcontext.ValidUCITransportSession(transportSession) {
			sessionBinding = "transport"
		}
		question := ""
		if fileConflict {
			question = "The portable marker and shared Git cache identify different existing projects. Which project should this session use for the pending safe memory read?"
		} else if legacyCacheConflict {
			question = "The server-confirmed legacy mapping and shared Git cache identify different existing projects. Which project should this session use for the pending safe memory read?"
		} else if originConflict {
			question = "The server reported a primary-origin conflict but did not identify its candidates. Which currently authorized existing project should this worktree use?"
		} else if conflictingMappings {
			question = "The existing historical mapping and this worktree's primary origin identify different projects. Which of these two existing projects should this worktree use?"
		}
		status, instructions := "choices_available", ""
		if conflictingMappings || originConflict {
			status = "user_input_required"
			instructions = "Ask the human the question in this host conversation, then call project_context repair with the selected fresh selection_ref. Resume only the original safe read."
		}
		payload, err := json.Marshal(map[string]any{"status": status, "choices": choices, "historical_mapping_confirmed": legacyConfirmed, "repository_mapping_confirmed": repositoryConfirmed, "local_seed_available": seedConfirmed, "mapping_conflict": conflictingMappings || originConflict, "origin_candidates_identified": !originConflict, "question": question, "instructions": instructions, "resume_action": "project_context repair", "session_binding": sessionBinding})
		if err != nil {
			return nil, err
		}
		return buildInnerBlock(payload)
	}
	var selected projectidentity.ExistingProjectChoice
	var chosenEvidence projectChoiceEvidence
	if request.SelectionRef == "" {
		if !confirmed {
			return nil, &module.ModuleError{Code: "PROJECT_SELECTION_REQUIRED", Message: "Call project_context choices and ask the human in this host conversation which displayed existing project this worktree should use; pass its fresh selection_ref to repair. This does not migrate history."}
		}
		for _, item := range catalog {
			if item.ProjectID == historical.ProjectID {
				selected = item
				break
			}
		}
	} else {
		m.projectChoicesMu.Lock()
		evidence, ok := m.projectChoices[request.SelectionRef]
		m.projectChoicesMu.Unlock()
		if !ok || time.Now().After(evidence.expires) || evidence.scope != scope || evidence.marker != state || evidence.catalog != catalogDigest {
			return nil, &module.ModuleError{Code: "PROJECT_SELECTION_STALE", Message: "Choice is stale or belongs to another caller. Request fresh choices for this root."}
		}
		chosenEvidence = evidence
		selected = evidence.project
	}
	if localConflict {
		if selected.ProjectID != historical.ProjectID && selected.ProjectID != state.CacheID {
			return nil, &module.ModuleError{Code: "PROJECT_SELECTION_STALE", Message: "Select one of the fresh authorized conflicting candidates."}
		}
		if !auditcontext.ValidUCITransportSession(transportSession) {
			return nil, &module.ModuleError{Code: "PROJECT_SESSION_BINDING_UNAVAILABLE", Message: "This transport does not provide an isolated session identity. Preserve both files; reconnect through a transport that supplies an authorized session identity, then obtain fresh choices. No safe scoped continuation is available in this session."}
		}
		m.projectChoicesMu.Lock()
		pending, ok := m.projectPendingReads[scope]
		liveChoice, choiceOK := m.projectChoices[request.SelectionRef]
		if !ok || !choiceOK || liveChoice != chosenEvidence || time.Now().After(pending.expires) || time.Now().After(liveChoice.expires) || pending.marker != state || legacyCacheConflict && pending.historicalID != historical.ProjectID || chosenEvidence.requestNonce == "" || pending.requestNonce != chosenEvidence.requestNonce || pending.requestDigest != chosenEvidence.requestDigest {
			m.projectChoicesMu.Unlock()
			return nil, &module.ModuleError{Code: "PROJECT_SELECTION_STALE", Message: "No matching pending safe recall in this session. Retry the original recall_memory once, then obtain fresh choices."}
		}
		if m.projectReadBindings == nil {
			m.projectReadBindings = make(map[string]projectChoiceEvidence)
		}
		for key, entry := range m.projectReadBindings {
			if time.Now().After(entry.expires) {
				delete(m.projectReadBindings, key)
			}
		}
		if _, exists := m.projectReadBindings[scope]; !exists && len(m.projectReadBindings) >= 4096 {
			m.projectChoicesMu.Unlock()
			return nil, &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: "Too many active scoped reads; retry after they expire."}
		}
		pending.project = selected
		m.projectReadBindings[scope] = pending
		delete(m.projectPendingReads, scope)
		delete(m.projectChoices, request.SelectionRef)
		m.projectChoicesMu.Unlock()
		payload, err := json.Marshal(map[string]any{"status": "session_read_bound", "project_id": selected.ProjectID, "resume_action": "retry the original recall_memory once with project omitted", "instructions": "Both files remain unchanged. This one-use binding applies only to recall_memory in this exact transport session and caller scope; it expires in five minutes."})
		if err != nil {
			return nil, err
		}
		return buildInnerBlock(payload)
	}
	if selected.ProjectID == "" || state.PinnedID != "" && state.PinnedID != selected.ProjectID || confirmed && historical.ProjectID != selected.ProjectID || conflictingMappings && selected.ProjectID != historical.ProjectID && selected.ProjectID != repository.ProjectID {
		return nil, &module.ModuleError{Code: "PROJECT_IDENTITY_CONFLICT", Message: "The selected project conflicts with pinned canonical evidence. Preserve the marker and cache; use project_context choices in this host conversation or request access to the intended project before continuing."}
	}
	marker, err := json.Marshal(projectidentity.Project{ProjectID: selected.ProjectID, Name: selected.Name})
	if err != nil {
		return nil, err
	}
	precondition := map[string]string{"state": "absent"}
	if state.Present {
		precondition = map[string]string{"state": "sha256", "sha256": state.MarkerDigest}
	}
	action := map[string]any{"project_id": selected.ProjectID, "target": state.Target, "caller_root_sha256": state.RootDigest, "target_root_sha256": state.TargetRootDigest, "marker_content": string(marker) + "\n", "precondition": precondition, "historical_membership_changed": false, "instructions": projectidentity.MarkerActionGuidance}
	if seedConfirmed {
		action["mapping_source"] = "approved_local_seed"
		action["v2_precondition"] = map[string]string{"target": seedTarget, "sha256": seedDigest}
	}
	payload, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	return buildInnerBlock(payload)
}
