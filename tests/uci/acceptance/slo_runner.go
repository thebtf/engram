package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thebtf/engram/internal/uci"
)

const uciSLOInputPathEnv = "ENGRAM_UCI_SLO_INPUT"

// RunUCISLOProfile reads one externally recorded exact-candidate measurement
// input and deterministically accounts for it. It never runs a benchmark,
// derives identities from the host, or creates a passing measurement when the
// external input is absent.
func RunUCISLOProfile(ctx context.Context) (uci.UCISLOReport, error) {
	if err := ctx.Err(); err != nil {
		return uci.UCISLOReport{}, fmt.Errorf("UCI SLO report context: %w", err)
	}

	path := strings.TrimSpace(os.Getenv(uciSLOInputPathEnv))
	if path == "" {
		return uci.UCISLOReport{}, fmt.Errorf("UCI SLO report requires externally recorded input via %s; refusing to fabricate samples or a measured pass", uciSLOInputPathEnv)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return uci.UCISLOReport{}, fmt.Errorf("read UCI SLO input %q: %w", path, err)
	}

	var input uci.UCISLOMeasurementInput
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return uci.UCISLOReport{}, fmt.Errorf("decode UCI SLO input %q: %w", path, err)
	}
	if err := ensureUCISLOInputEOF(decoder); err != nil {
		return uci.UCISLOReport{}, fmt.Errorf("decode UCI SLO input %q: %w", path, err)
	}

	report, err := uci.CalculateUCISLOReport(input)
	if err != nil {
		return report, fmt.Errorf("calculate UCI SLO report from %q: %w", path, err)
	}
	if violations := uci.ValidateUCISLOReport(report); len(violations) != 0 {
		return report, fmt.Errorf("externally recorded UCI SLO input %q is not an accepted healthy-profile result: %s", path, uciSLOViolationCodes(violations))
	}
	return report, nil
}

func ensureUCISLOInputEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("input contains more than one JSON value")
		}
		return err
	}
	return nil
}

func uciSLOViolationCodes(violations []uci.UCISLOViolation) string {
	codes := make([]string, len(violations))
	for index, violation := range violations {
		codes[index] = violation.Code
	}
	return strings.Join(codes, ", ")
}
