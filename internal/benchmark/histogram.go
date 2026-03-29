//go:build benchmark

package benchmark

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type Histogram struct {
	samples []time.Duration
}

func NewHistogram() *Histogram {
	return &Histogram{}
}

func (h *Histogram) Add(d time.Duration) {
	h.samples = append(h.samples, d)
}

func (h *Histogram) Percentile(p float64) time.Duration {
	if len(h.samples) == 0 {
		return 0
	}

	samples := h.sortedSamples()
	idx := int(p * float64(len(samples)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

func (h *Histogram) Mean() time.Duration {
	if len(h.samples) == 0 {
		return 0
	}

	var total time.Duration
	for _, sample := range h.samples {
		total += sample
	}

	return total / time.Duration(len(h.samples))
}

func (h *Histogram) Max() time.Duration {
	if len(h.samples) == 0 {
		return 0
	}

	max := h.samples[0]
	for _, sample := range h.samples[1:] {
		if sample > max {
			max = sample
		}
	}
	return max
}

func (h *Histogram) Stddev() time.Duration {
	if len(h.samples) == 0 {
		return 0
	}

	mean := float64(h.Mean())
	var sumSq float64
	for _, sample := range h.samples {
		d := float64(sample) - mean
		sumSq += d * d
	}
	variance := sumSq / float64(len(h.samples))
	if variance < 0 {
		variance = 0
	}
	return time.Duration(math.Sqrt(variance))
}

func (h *Histogram) Print(label string) {
	p50 := h.Percentile(0.50)
	p95 := h.Percentile(0.95)
	p99 := h.Percentile(0.99)
	max := h.Max()
	mean := h.Mean()

	fmt.Printf("%-30s p50=%8s p95=%8s p99=%8s max=%8s mean=%8s n=%d\n", label, p50, p95, p99, max, mean, len(h.samples))
}

func (h *Histogram) sortedSamples() []time.Duration {
	copySamples := append([]time.Duration(nil), h.samples...)
	sort.Slice(copySamples, func(i, j int) bool {
		return copySamples[i] < copySamples[j]
	})
	return copySamples
}
