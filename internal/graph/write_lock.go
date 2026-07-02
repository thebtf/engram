package graph

import "sync"

var graphWriteMu sync.Mutex

// LockWrites serializes graph mutations within the worker process so
// duplicate-edge validation and cascade delete checks can observe a stable
// write order across REST, MCP, and write-lint callers.
func LockWrites() func() {
	graphWriteMu.Lock()
	return graphWriteMu.Unlock
}
