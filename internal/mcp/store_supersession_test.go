// Package mcp provides the MCP server for engram.
// store_supersession_test.go previously contained integration tests for vector-based
// observation supersession. That observation-vector subsystem was retired in v5, so
// those tests are no longer applicable. (The content_chunks table was later restored
// at migration 108 for vNext memory embeddings — a different, memory-keyed schema.)
package mcp
