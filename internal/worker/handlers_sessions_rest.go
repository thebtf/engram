// Package worker — indexed session REST handlers removed in v5 (US3).
// handleListIndexedSessions and handleSearchIndexedSessions depended on the
// sessions.Store/IndexedSession types which were deleted in chunk 1.
// The routes /api/sessions-index and /api/sessions-index/search were removed
// from service.go in chunk 2.
package worker
