package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/redaction"
)

const (
	interventionReconcilerInterval = time.Minute
	interventionReconcilerTimeout  = 5 * time.Second
	interventionReconcilerLimit    = 128
)

// policyReconciler keeps the Service field bound to the domain-owned contract
// without exposing the concrete reconciler implementation to the worker.
type policyReconciler = intervention.PolicyReconciler

type interventionReconcilerTicker interface {
	C() <-chan time.Time
	Stop()
}

type interventionReconcilerTickerFactory func(time.Duration) interventionReconcilerTicker

type standardInterventionReconcilerTicker struct {
	*time.Ticker
}

func (t standardInterventionReconcilerTicker) C() <-chan time.Time {
	return t.Ticker.C
}

func newInterventionReconcilerTicker(interval time.Duration) interventionReconcilerTicker {
	return standardInterventionReconcilerTicker{Ticker: time.NewTicker(interval)}
}

// SetInterventionReconciler publishes the optional automatic policy reconciler.
// Construction occurs during asynchronous initialization; it deliberately does
// not activate any request-path advisor or gRPC wiring.
func (s *Service) SetInterventionReconciler(reconciler intervention.PolicyReconciler) {
	s.initMu.Lock()
	s.interventionReconciler = reconciler
	s.initMu.Unlock()
}

// initializeInterventionReconciler wires only the automatic compiler/reconciler
// path. Its dependencies are optional at startup: when an existing Vault epoch
// or database dependency is unavailable, the server remains ready without a
// reconciler and the typed gRPC advisor stays default-dark.
func (s *Service) initializeInterventionReconciler(store *gorm.Store) {
	reconciler, err := s.newInterventionPolicyReconciler(store)
	if err != nil {
		log.Warn().Err(err).Msg("intervention policy reconciliation disabled")
		return
	}

	s.SetInterventionReconciler(reconciler)
	s.startInterventionReconciler()
}

func (s *Service) newInterventionPolicyReconciler(store *gorm.Store) (intervention.PolicyReconciler, error) {
	ctx := s.ctx
	cfg := s.config
	if ctx == nil {
		return nil, fmt.Errorf("intervention policy reconciler: missing root context")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("intervention policy reconciler: root context unavailable: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("intervention policy reconciler: missing configuration")
	}
	if store == nil {
		return nil, fmt.Errorf("intervention policy reconciler: missing database store")
	}
	db := store.GetDB()
	if db == nil {
		return nil, fmt.Errorf("intervention policy reconciler: missing database store")
	}

	receiptStore := gorm.NewInterventionReceiptStore(db)
	keyProvider, err := openInterventionKeyProvider(ctx, cfg, receiptStore)
	if err != nil {
		return nil, fmt.Errorf("open intervention key provider: %w", err)
	}

	policyStore := gorm.NewInterventionPolicyStore(db)

	// The compiler receives one ordered snapshot. Keep the Service-owned slice
	// available unchanged for existing vNext consumers.
	s.initMu.RLock()
	compilerRules := append([]redaction.CompiledRule(nil), s.redactionRules...)
	s.initMu.RUnlock()
	compiler, err := intervention.NewPolicyCompiler(intervention.PolicyCompilerConfig{
		KeyProvider:    keyProvider,
		RedactionRules: compilerRules,
	})
	if err != nil {
		return nil, fmt.Errorf("new intervention policy compiler: %w", err)
	}

	reconciler, err := intervention.NewPolicyReconciler(intervention.PolicyReconcilerConfig{
		Repository: policyStore,
		Compiler:   compiler,
	})
	if err != nil {
		return nil, fmt.Errorf("new intervention policy reconciler: %w", err)
	}
	return reconciler, nil
}

// startInterventionReconciler launches one Service-owned worker after all of
// its dependencies have been published. The first bounded pass runs immediately;
// subsequent passes run at the fixed production interval until Service shutdown.
func (s *Service) startInterventionReconciler() {
	s.initMu.RLock()
	reconciler := s.interventionReconciler
	rootCtx := s.ctx
	newTicker := s.interventionReconcilerTickerFactory
	s.initMu.RUnlock()

	if reconciler == nil {
		return
	}
	if rootCtx == nil {
		log.Warn().Msg("intervention policy reconciliation disabled: missing root context")
		return
	}
	if err := rootCtx.Err(); err != nil {
		return
	}
	if newTicker == nil {
		newTicker = newInterventionReconcilerTicker
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		s.runInterventionReconcilerPass(rootCtx, reconciler)
		if rootCtx.Err() != nil {
			return
		}

		ticker := newTicker(interventionReconcilerInterval)
		if ticker == nil {
			log.Warn().Msg("intervention policy reconciliation stopped: ticker unavailable")
			return
		}
		defer ticker.Stop()

		for {
			select {
			case <-rootCtx.Done():
				return
			case <-ticker.C():
				s.runInterventionReconcilerPass(rootCtx, reconciler)
			}
		}
	}()
}

func (s *Service) runInterventionReconcilerPass(ctx context.Context, reconciler intervention.PolicyReconciler) {
	passCtx, cancel := context.WithTimeout(ctx, interventionReconcilerTimeout)
	defer cancel()

	if _, err := reconciler.Reconcile(passCtx, interventionReconcilerLimit); err != nil {
		log.Warn().Err(err).Msg("intervention policy reconciliation pass failed; will retry")
	}
}
