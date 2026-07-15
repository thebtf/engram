package s4bsurfacing

import (
	"context"
	"errors"
	"reflect"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	defaultProposalLimit = 10
	maxProposalLimit     = 25
)

type Proposer struct {
	source Source
	meter  cognitivecore.SubsystemMeter
}

var _ cognitive.CandidateProposer = (*Proposer)(nil)

func NewProposer(source Source) *Proposer {
	return &Proposer{source: source}
}

func (p *Proposer) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	if p == nil || !hasEffectiveSource(p.source) {
		return nil, ErrNoSource
	}
	p.incr("calls_total", 1, "propose")
	if err := ctx.Err(); err != nil {
		p.incrError(contextErrorKind(err))
		return nil, err
	}

	matchCtx := newMatchContext(event)
	if matchCtx.eventType == "" || matchCtx.project == "" || matchCtx.session == "" || len(matchCtx.tokens) == 0 {
		p.incrStage("event_dropped", 1, "empty")
		return []cognitive.HintProposal{}, nil
	}

	events, err := p.source.ListByProject(ctx, matchCtx.project, sourceScanLimit)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			p.incrError(contextErrorKind(ctxErr))
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) {
			p.incrError("canceled")
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			p.incrError("deadline")
			return nil, context.DeadlineExceeded
		}
		p.incrError("source")
		return nil, &SourceError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		p.incrError(contextErrorKind(err))
		return nil, err
	}

	p.incrStage("event_emitted", uint64(len(events)), "scanned")
	matches := make([]matchedDirective, 0, len(events))
	for i := range events {
		if err := ctx.Err(); err != nil {
			p.incrError(contextErrorKind(err))
			return nil, err
		}
		if match, ok := matchDirective(matchCtx, events[i]); ok {
			matches = append(matches, match)
		}
	}
	p.incrStage("event_dropped", uint64(len(events)-len(matches)), "skipped")

	if err := ctx.Err(); err != nil {
		p.incrError(contextErrorKind(err))
		return nil, err
	}
	sortMatches(matches)
	limit = normalizeProposalLimit(limit)
	if dropped := len(matches) - limit; dropped > 0 {
		p.incrStage("event_dropped", uint64(dropped), "limit")
		matches = matches[:limit]
	}

	proposals := make([]cognitive.HintProposal, 0, len(matches))
	for _, match := range matches {
		if err := ctx.Err(); err != nil {
			p.incrError(contextErrorKind(err))
			return nil, err
		}
		proposals = append(proposals, match.proposal())
	}
	if len(proposals) == 0 {
		p.incrStage("event_dropped", 1, "empty")
	}
	p.incrStage("event_emitted", uint64(len(proposals)), "proposed")
	return proposals, nil
}

func (p *Proposer) setMeter(meter cognitivecore.SubsystemMeter) {
	if p == nil {
		return
	}
	if !hasEffectiveMeter(meter) {
		p.meter = nil
		return
	}
	p.meter = meter
}

func (p *Proposer) incr(name string, delta uint64, operation string) {
	if p == nil || p.meter == nil {
		return
	}
	p.meter.IncrCounter(name, delta, map[string]string{
		"operation": operation,
		"subsystem": subsystemName,
	})
}

func (p *Proposer) incrStage(name string, delta uint64, stage string) {
	if p == nil || p.meter == nil {
		return
	}
	p.meter.IncrCounter(name, delta, map[string]string{
		"stage":     stage,
		"subsystem": subsystemName,
	})
}

func (p *Proposer) incrError(kind string) {
	if p == nil || p.meter == nil {
		return
	}
	p.meter.IncrCounter("errors_total", 1, map[string]string{
		"kind":      kind,
		"subsystem": subsystemName,
	})
}

func contextErrorKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	return "canceled"
}

func hasEffectiveSource(source Source) bool {
	if source == nil {
		return false
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func hasEffectiveMeter(meter cognitivecore.SubsystemMeter) bool {
	if meter == nil {
		return false
	}
	value := reflect.ValueOf(meter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func normalizeProposalLimit(limit int) int {
	if limit <= 0 {
		return defaultProposalLimit
	}
	if limit > maxProposalLimit {
		return maxProposalLimit
	}
	return limit
}
