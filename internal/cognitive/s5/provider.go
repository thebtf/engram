package s5

import (
	"context"
	"errors"

	"github.com/thebtf/engram/internal/cognitive/core"
)

var ErrInvalidWindow = errors.New("s5: invalid product metrics window")

type MetricSample struct {
	Metric  string
	Value   float64
	SampleN uint64
}

type MetricSource interface {
	ProductMetricSamples(ctx context.Context, window core.ProductMetricsWindow) ([]MetricSample, error)
}

type Dependencies struct {
	MetricSources       []MetricSource
	ReadinessThresholds map[string]uint64
}

type Provider struct {
	deps Dependencies
}

var (
	_ core.Subsystem              = (*Provider)(nil)
	_ core.ProductMetricsProvider = (*Provider)(nil)
)

func NewProvider(deps Dependencies) *Provider {
	return &Provider{deps: cloneDependencies(deps)}
}

func (p *Provider) Name() string {
	return "engram.s5.product_metrics"
}

func (p *Provider) Version() string {
	return "v1.0.0"
}

func (p *Provider) Start(_ context.Context, _ core.Dependencies) error {
	return nil
}

func (p *Provider) Stop() error {
	return nil
}

func (p *Provider) Implements() []string {
	return []string{"ProductMetricsProvider"}
}

func (p *Provider) ProductMetrics(ctx context.Context, window core.ProductMetricsWindow) (core.ProductMetricsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return core.ProductMetricsSnapshot{}, err
	}
	if !window.Since.IsZero() && !window.Until.IsZero() && window.Since.After(window.Until) {
		return core.ProductMetricsSnapshot{}, ErrInvalidWindow
	}

	samplesByMetric, err := p.collectSamples(ctx, window)
	if err != nil {
		return core.ProductMetricsSnapshot{}, err
	}

	canonicalKeys := CanonicalMetricKeys()
	snap := core.ProductMetricsSnapshot{
		Window:    window,
		Metrics:   emptyMetrics(),
		SampleN:   0,
		Readiness: make(map[string]core.ProductMetricReadiness, len(canonicalKeys)),
	}
	for _, metric := range canonicalKeys {
		threshold := p.deps.ReadinessThresholds[metric]
		readiness := core.ProductMetricReadiness{ThresholdN: threshold}
		sample, ok := samplesByMetric[metric]
		if ok {
			readiness.SampleN = sample.SampleN
			if sample.SampleN > snap.SampleN {
				snap.SampleN = sample.SampleN
			}
		}

		switch {
		case !ok || sample.SampleN == 0:
			readiness.State = readinessStateNoSample
		case threshold > 0 && sample.SampleN < threshold:
			readiness.State = readinessStateBelowThreshold
		default:
			readiness.State = readinessStateReady
			snap.Metrics[metric] = sample.Value
		}
		snap.Readiness[metric] = readiness
	}

	return snap, nil
}

func (p *Provider) collectSamples(ctx context.Context, window core.ProductMetricsWindow) (map[string]MetricSample, error) {
	byMetric := make(map[string]MetricSample, len(p.deps.MetricSources))
	for _, source := range p.deps.MetricSources {
		if source == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		samples, err := source.ProductMetricSamples(ctx, window)
		if err != nil {
			return nil, err
		}
		for _, sample := range samples {
			if !isCanonicalMetric(sample.Metric) {
				continue
			}
			current, exists := byMetric[sample.Metric]
			if !exists || sample.SampleN >= current.SampleN {
				byMetric[sample.Metric] = sample
			}
		}
	}
	return byMetric, nil
}

func cloneDependencies(deps Dependencies) Dependencies {
	cloned := Dependencies{}
	if len(deps.MetricSources) > 0 {
		cloned.MetricSources = append([]MetricSource(nil), deps.MetricSources...)
	}
	if len(deps.ReadinessThresholds) > 0 {
		cloned.ReadinessThresholds = make(map[string]uint64, len(deps.ReadinessThresholds))
		for metric, threshold := range deps.ReadinessThresholds {
			cloned.ReadinessThresholds[metric] = threshold
		}
	}
	return cloned
}
