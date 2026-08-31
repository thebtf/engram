package intervention

import "context"

const maxPolicyReconcileLimit = 128

// ReconcileResult records one bounded policy reconciliation pass.
type ReconcileResult struct {
	Scanned      int
	Inserted     int
	Existing     int
	Insufficient int
	Stale        int
}

// PolicyReconciler is the bounded worker-facing policy reconciliation port.
type PolicyReconciler interface {
	Reconcile(context.Context, int) (ReconcileResult, error)
}

// PolicyReconcilerConfig supplies the source repository and deterministic compiler.
type PolicyReconcilerConfig struct {
	Repository PolicyRepository
	Compiler   *PolicyCompiler
}

type policyReconciler struct {
	repository PolicyRepository
	compiler   *PolicyCompiler
}

var _ PolicyReconciler = (*policyReconciler)(nil)

// NewPolicyReconciler constructs one bounded, idempotent reconciler.
func NewPolicyReconciler(config PolicyReconcilerConfig) (PolicyReconciler, error) {
	if config.Repository == nil || config.Compiler == nil {
		return nil, ErrInvalidInput
	}
	return &policyReconciler{repository: config.Repository, compiler: config.Compiler}, nil
}

// Reconcile compiles and commits at most 128 current source versions. A stale
// source is reported when CommitPolicy returns its zero policy without an error.
func (r *policyReconciler) Reconcile(ctx context.Context, limit int) (ReconcileResult, error) {
	if r == nil || r.repository == nil || r.compiler == nil || ctx == nil {
		return ReconcileResult{}, ErrInvalidInput
	}
	limit = normalizePolicyReconcileLimit(limit)
	sources, err := r.repository.ListCompileSources(ctx, r.compiler.Versions(), limit)
	if err != nil {
		return ReconcileResult{}, err
	}
	if len(sources) > limit {
		return ReconcileResult{}, ErrInvalidInput
	}

	result := ReconcileResult{Scanned: len(sources)}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		definition, err := r.compiler.Compile(source)
		if err != nil {
			return result, err
		}
		if !definition.CanCommit() {
			return result, ErrInvalidInput
		}
		if definition.record.DescriptorStatus == PolicyDescriptorInsufficient {
			result.Insufficient++
		}
		winner, inserted, err := r.repository.CommitPolicy(ctx, definition)
		if err != nil {
			return result, err
		}
		if inserted {
			result.Inserted++
			continue
		}
		if winner.exists() {
			result.Existing++
			continue
		}
		result.Stale++
	}
	return result, nil
}

func normalizePolicyReconcileLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > maxPolicyReconcileLimit {
		return maxPolicyReconcileLimit
	}
	return limit
}
