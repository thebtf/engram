// Package main wires the modular daemon framework together. Registration
// of individual modules lives here (one call per module) so the framework
// itself stays module-agnostic and main.go stays ~boot-only.
//
// Design reference: design.md §4.1 (boot sequence) and tasks T040/T041.
package main

import (
	"fmt"

	"github.com/thebtf/engram/internal/handlers/engramcore"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
	"github.com/thebtf/engram/internal/module/registry"
)

// registerModules creates and registers every module that ships with the
// engram daemon. Called from main() BEFORE Freeze and lifecycle.Pipeline.Start.
//
// In v4.3.0 the only module is engramcore (the ProxyToolProvider wrapping the
// legacy engramHandler). Future phases add modules here:
//
//	Phase B (loom integration):          loom.NewModule()
//	Phase D1 (vectorindex):              vectorindex.NewModule(cfg)
//	Phase D2 (semantic-refactor):        semrefactor.NewModule(cfg)
//
// Keep this function small and explicit — no reflection, no config-driven
// registration lists. One line per module, per design.md §2.3.
func registerModules(reg *registry.Registry) error {
	if err := reg.Register(engramcore.NewModule()); err != nil {
		return fmt.Errorf("register engramcore: %w", err)
	}
	if err := reg.Register(loomhandler.NewModule()); err != nil {
		return fmt.Errorf("register loom: %w", err)
	}
	return nil
}
