# Tasks: Embedding Resilience Layer

**Generated:** 2026-03-29

## Phase 1: EmbeddingResilience struct

- [x] T001 Create `internal/embedding/resilience.go` — ResilientEmbedder with 4 states, atomic transitions, threshold logic, health check goroutine
- [x] T002 Run `go build ./...` to verify

---

## Phase 2: Wire into service

- [x] T003 Replace direct embedder in worker service with ResilientEmbedder wrapper in `internal/worker/service.go`
- [x] T004 Add embedding_status to selfcheck handler in `internal/worker/handlers.go`
- [x] T005 Run `go build ./...` to verify

---

## Phase 3: Release

- [x] T006 Create PR, run review, merge
- [x] T007 Tag release
