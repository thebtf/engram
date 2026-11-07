package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/internal/taskcontext"
)

type taskKnowledgeResult struct {
	kind   string
	status string
	items  []taskcontext.Evidence
}

func taskKnowledgeStatus(ctx context.Context, err error, count int) string {
	if ctx.Err() != nil {
		return "deadline_exceeded"
	}
	if err != nil {
		return "unavailable"
	}
	if count == 0 {
		return "empty"
	}
	return "ok"
}

func taskEvidence(kind string, id int64, version int, path, content, expectedHash string) (taskcontext.Evidence, bool) {
	if (kind != "memory" && kind != "note" && kind != "rule") || id <= 0 || id > 9007199254740991 || version < 0 || (kind == "note" && (version == 0 || path == "" || expectedHash == "")) || len(path) > 4096 || !utf8.ValidString(content) || strings.TrimSpace(content) == "" || !utf8.ValidString(path) {
		return taskcontext.Evidence{}, false
	}
	digest := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(digest[:])
	if expectedHash != "" && expectedHash != hash {
		return taskcontext.Evidence{}, false
	}
	fragment := content
	if len(fragment) > 4000 {
		fragment = fragment[:4000]
		for !utf8.ValidString(fragment) {
			fragment = fragment[:len(fragment)-1]
		}
	}
	fragmentDigest := sha256.Sum256([]byte(fragment))
	return taskcontext.Evidence{Kind: kind, ID: id, Version: version, Path: path, Content: fragment, ContentHash: hex.EncodeToString(fragmentDigest[:]), SourceHash: hash, Excerpt: len(fragment) != len(content)}, true
}

func (server *Server) taskMemories(ctx context.Context, project, query string) taskKnowledgeResult {
	result := taskKnowledgeResult{kind: "memory", status: "unavailable"}
	if server.memoryStore == nil {
		return result
	}
	caller := scope.KeycardContext{}
	if identity, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = identity.WorkstationID()
		caller.Principal = identity.Principal
		caller.PrincipalKind = string(identity.PrincipalKind)
	}
	rows, _, err := server.memoryStore.SearchFTSForSearch(ctx, project, query, 3, scope.MemoryReadAccess{Caller: caller, Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}})
	if err != nil {
		result.status = taskKnowledgeStatus(ctx, err, 0)
		return result
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		if item, ok := taskEvidence("memory", row.ID, row.Version, "", row.Content, ""); ok {
			result.items = append(result.items, item)
		}
	}
	result.status = taskKnowledgeStatus(ctx, err, len(result.items))
	if result.status == "deadline_exceeded" {
		result.items = nil
	}
	if len(rows) > 0 && len(result.items) == 0 && result.status == "empty" {
		result.status = "unavailable"
	}
	return result
}

func (server *Server) taskNotes(ctx context.Context, project, query string) taskKnowledgeResult {
	result := taskKnowledgeResult{kind: "note", status: "unavailable"}
	if server.versionedDocumentStore == nil {
		return result
	}
	rows, err := server.versionedDocumentStore.SearchTaskContext(ctx, project, query, 3)
	if err != nil {
		result.status = taskKnowledgeStatus(ctx, err, 0)
		return result
	}
	for _, row := range rows {
		if item, ok := taskEvidence("note", row.ID, row.Version, row.Path, row.Content, row.ContentHash); ok {
			result.items = append(result.items, item)
		}
	}
	result.status = taskKnowledgeStatus(ctx, err, len(result.items))
	if result.status == "deadline_exceeded" {
		result.items = nil
	}
	if len(rows) > 0 && len(result.items) == 0 && result.status == "empty" {
		result.status = "unavailable"
	}
	return result
}

func (server *Server) taskRules(ctx context.Context, project string) taskKnowledgeResult {
	result := taskKnowledgeResult{kind: "rule", status: "unavailable"}
	if server.behavioralRulesStore == nil {
		return result
	}
	rows, err := server.behavioralRulesStore.ListEnabled(ctx, &project, 3)
	if err != nil {
		result.status = taskKnowledgeStatus(ctx, err, 0)
		return result
	}
	for _, row := range rows {
		if row != nil {
			if item, ok := taskEvidence("rule", row.ID, row.Version, "", row.Content, ""); ok {
				result.items = append(result.items, item)
			}
		}
	}
	result.status = taskKnowledgeStatus(ctx, nil, len(result.items))
	if result.status == "deadline_exceeded" {
		result.items = nil
	}
	if len(rows) > 0 && len(result.items) == 0 && result.status == "empty" {
		result.status = "unavailable"
	}
	return result
}
