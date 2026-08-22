package operability

import (
	"reflect"
	"testing"
)

func TestEvaluateMetricZeroDenominatorIsNotComputable(t *testing.T) {
	metric, err := EvaluateMetric(metricInput(DurableRecordSource, 0))
	if err != nil {
		t.Fatal(err)
	}
	if metric.ResultStatus != NotComputable || metric.Ratio != nil {
		t.Fatalf("zero denominator=%#v, want not_computable without a ratio", metric)
	}
}

func TestEvaluateMetricRequiresNamedSemantics(t *testing.T) {
	input := metricInput(DurableRecordSource, 4)
	input.Freshness = ""
	if _, err := EvaluateMetric(input); err == nil {
		t.Fatal("metric without freshness was accepted")
	}
	input = metricInput(DurableRecordSource, 4)
	input.NumeratorValue = -1
	if _, err := EvaluateMetric(input); err == nil {
		t.Fatal("metric with negative numerator was accepted")
	}
}

func TestBuildBaselineReportSeparatesDurableAndProcessFamilies(t *testing.T) {
	durable := metricInput(DurableRecordSource, 4)
	durable.Name = "z-durable"
	process := metricInput(ProcessCounterSource, 2)
	process.Name = "a-process"
	report, err := BuildBaselineReport([]MetricInput{durable}, []MetricInput{process})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DurableFacts) != 1 || len(report.ProcessCounters) != 1 {
		t.Fatalf("families were not retained separately: %#v", report)
	}
	if report.DurableFacts[0].Source != DurableRecordSource || report.ProcessCounters[0].Source != ProcessCounterSource {
		t.Fatalf("report changed metric authorities: %#v", report)
	}
	if _, err := BuildBaselineReport([]MetricInput{process}, nil); err == nil {
		t.Fatal("process counter entered durable baseline facts")
	}
}

func TestBuildBaselineReportIsDeterministicallyOrdered(t *testing.T) {
	first := metricInput(DurableRecordSource, 1)
	first.Name = "zeta"
	second := metricInput(DurableRecordSource, 1)
	second.Name = "alpha"
	report, err := BuildBaselineReport([]MetricInput{first, second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{report.DurableFacts[0].Name, report.DurableFacts[1].Name}
	if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("metric order=%v want=%v", got, want)
	}
}

func metricInput(source MetricSource, denominator int64) MetricInput {
	return MetricInput{
		Name:             "coverage",
		NumeratorName:    "accepted records",
		NumeratorValue:   3,
		DenominatorName:  "observed records",
		DenominatorValue: denominator,
		Scope:            "synthetic fixture",
		Window:           "fixture run",
		Freshness:        "captured at fixture completion",
		Source:           source,
	}
}
