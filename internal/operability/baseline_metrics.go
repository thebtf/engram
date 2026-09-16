// Package operability models read-only baseline evidence.
package operability

import (
	"fmt"
	"sort"
	"strings"
)

// MetricSource identifies the authoritative family of a metric observation.
type MetricSource string

const (
	DurableRecordSource  MetricSource = "durable-record"
	ProcessCounterSource MetricSource = "process-counter"
)

// ResultStatus is deliberately not a health verdict. In particular, a zero
// denominator is not a successful result.
type ResultStatus string

const (
	Computed      ResultStatus = "computed"
	NotComputable ResultStatus = "not_computable"
)

// MetricInput is a named, read-only measurement submitted to the baseline
// report. Values are caller-provided observations; this package does not read
// process counters, databases, or runtime configuration.
type MetricInput struct {
	Name             string
	NumeratorName    string
	NumeratorValue   int64
	DenominatorName  string
	DenominatorValue int64
	Scope            string
	Window           string
	Freshness        string
	Source           MetricSource
}

// Metric is the read-only report representation of a metric input.
type Metric struct {
	Name             string       `json:"name"`
	NumeratorName    string       `json:"numerator_name"`
	NumeratorValue   int64        `json:"numerator_value"`
	DenominatorName  string       `json:"denominator_name"`
	DenominatorValue int64        `json:"denominator_value"`
	Scope            string       `json:"scope"`
	Window           string       `json:"window"`
	Freshness        string       `json:"freshness"`
	Source           MetricSource `json:"source"`
	ResultStatus     ResultStatus `json:"result_status"`
	Ratio            *float64     `json:"ratio,omitempty"`
}

// BaselineReport keeps durable baseline facts separate from process-lifetime
// counters. Combining those families requires a separately authored mapping;
// this read-only model does not perform that merge.
type BaselineReport struct {
	DurableFacts    []Metric `json:"durable_facts"`
	ProcessCounters []Metric `json:"process_counters"`
}

// EvaluateMetric validates a named measurement and makes zero denominators
// explicitly not_computable.
func EvaluateMetric(input MetricInput) (Metric, error) {
	if err := validateMetricInput(input); err != nil {
		return Metric{}, err
	}
	metric := Metric{
		Name:             input.Name,
		NumeratorName:    input.NumeratorName,
		NumeratorValue:   input.NumeratorValue,
		DenominatorName:  input.DenominatorName,
		DenominatorValue: input.DenominatorValue,
		Scope:            input.Scope,
		Window:           input.Window,
		Freshness:        input.Freshness,
		Source:           input.Source,
	}
	if input.DenominatorValue == 0 {
		metric.ResultStatus = NotComputable
		return metric, nil
	}
	ratio := float64(input.NumeratorValue) / float64(input.DenominatorValue)
	metric.ResultStatus = Computed
	metric.Ratio = &ratio
	return metric, nil
}

// BuildBaselineReport evaluates independent durable and process counter facts
// without merging their event windows or denominators.
func BuildBaselineReport(durable []MetricInput, process []MetricInput) (BaselineReport, error) {
	report := BaselineReport{
		DurableFacts:    make([]Metric, 0, len(durable)),
		ProcessCounters: make([]Metric, 0, len(process)),
	}
	for _, input := range durable {
		if input.Source != DurableRecordSource {
			return BaselineReport{}, fmt.Errorf("durable baseline %q must use %q source", input.Name, DurableRecordSource)
		}
		metric, err := EvaluateMetric(input)
		if err != nil {
			return BaselineReport{}, err
		}
		report.DurableFacts = append(report.DurableFacts, metric)
	}
	for _, input := range process {
		if input.Source != ProcessCounterSource {
			return BaselineReport{}, fmt.Errorf("process baseline %q must use %q source", input.Name, ProcessCounterSource)
		}
		metric, err := EvaluateMetric(input)
		if err != nil {
			return BaselineReport{}, err
		}
		report.ProcessCounters = append(report.ProcessCounters, metric)
	}
	sortMetrics(report.DurableFacts)
	sortMetrics(report.ProcessCounters)
	return report, nil
}

func validateMetricInput(input MetricInput) error {
	for _, required := range []struct {
		label string
		value string
	}{
		{"name", input.Name},
		{"numerator name", input.NumeratorName},
		{"denominator name", input.DenominatorName},
		{"scope", input.Scope},
		{"window", input.Window},
		{"freshness", input.Freshness},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("metric %s is required", required.label)
		}
	}
	if input.Source != DurableRecordSource && input.Source != ProcessCounterSource {
		return fmt.Errorf("metric %q has unknown source %q", input.Name, input.Source)
	}
	if input.NumeratorValue < 0 || input.DenominatorValue < 0 {
		return fmt.Errorf("metric %q cannot have negative values", input.Name)
	}
	return nil
}

func sortMetrics(metrics []Metric) {
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
}
