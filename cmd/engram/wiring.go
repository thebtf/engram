// Package main wires the modular daemon framework together. Registration
// of individual modules lives here (one call per module) so the framework
// itself stays module-agnostic and main.go stays ~boot-only.
//
// Design reference: design.md §4.1 (boot sequence) and tasks T040/T041.
package main

import (
	"fmt"
	"os"

	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
	"github.com/thebtf/engram/internal/module/registry"
)

// registerModules creates and registers every module that ships with the
// engram daemon. Called from main() BEFORE Freeze and lifecycle.Pipeline.Start.
//
// engramcore is constructed ONCE and shared with the codeintel module so the
// codeintel module can call IndexCodebase and proxy codebase_status to the
// server via the same gRPC connection pool.
//
// When ENGRAM_CODE_INTEL_ENABLED=true, the codeintel module is registered after
// engramcore. The single-ProxyToolProvider rule (FR-11a) is preserved: engramcore
// remains the only ProxyToolProvider. codeintel implements ToolProvider only
// (static tool list — codebase_index and codebase_status).
//
// Flag-OFF: codeintel is NOT registered; the daemon tool surface is byte-identical
// to pre-CR-006.
func registerModules(reg *registry.Registry) error {
	// Construct engramcore once — shared with codeintel below.
	coreModule := engramcore.NewModule()
	if err := reg.Register(coreModule); err != nil {
		return fmt.Errorf("register engramcore: %w", err)
	}
	if err := reg.Register(loomhandler.NewModule()); err != nil {
		return fmt.Errorf("register loom: %w", err)
	}

	// Register codeintel only when ENGRAM_CODE_INTEL_ENABLED=true.
	// The flag is checked here so the registry/dispatcher path is unchanged
	// when the flag is off — no tool conflict checks, no extra allocations.
	if os.Getenv("ENGRAM_CODE_INTEL_ENABLED") == "true" {
		if err := reg.Register(codeintel.NewModule(coreModule)); err != nil {
			return fmt.Errorf("register codeintel: %w", err)
		}
	}

	return nil
}
