package engramcore

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestProjectIdentityRefusalNeverRendersNativeCause(t *testing.T) {
	const cwd = "/private/synthetic-caller"
	const privatePath = cwd + "/.engram-project-private-token"
	ioError := func(op string) error {
		return &os.PathError{Op: op, Path: privatePath, Err: errors.New("private-credential private-marker-body")}
	}
	logical := &projectidentity.LogicalError{Code: "PROJECT_MIGRATION_REQUIRED"}
	for _, test := range []struct {
		name string
		err error
		code string
		guidanceCode string
	}{
		{"write", ioError("write"), "PROJECT_RESOLUTION_UNAVAILABLE", "PROJECT_RESOLUTION_UNAVAILABLE"},
		{"sync", ioError("sync"), "PROJECT_RESOLUTION_UNAVAILABLE", "PROJECT_RESOLUTION_UNAVAILABLE"},
		{"close", ioError("close"), "PROJECT_RESOLUTION_UNAVAILABLE", "PROJECT_RESOLUTION_UNAVAILABLE"},
		{"wrapped-io", fmt.Errorf("private-wrapper: %w", ioError("write")), "PROJECT_RESOLUTION_UNAVAILABLE", "PROJECT_RESOLUTION_UNAVAILABLE"},
		{"wrapped-logical", fmt.Errorf("private-wrapper private-credential: %w", logical), "PROJECT_MIGRATION_REQUIRED", "PROJECT_MIGRATION_REQUIRED"},
		{"joined-io-and-logical", errors.Join(ioError("sync"), logical), "PROJECT_MIGRATION_REQUIRED", "PROJECT_MIGRATION_REQUIRED"},
		{"wrapped-invalid", fmt.Errorf("private-wrapper: %w", &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_INVALID"}), "PROJECT_IDENTITY_INVALID", "PROJECT_IDENTITY_INVALID"},
		{"future-project-code", fmt.Errorf("private-wrapper: %w", &projectidentity.LogicalError{Code: "PROJECT_FUTURE_REFUSAL"}), "PROJECT_FUTURE_REFUSAL", "PROJECT_FUTURE_REFUSAL"},
		{"unknown-code", fmt.Errorf("private-wrapper: %w", &projectidentity.LogicalError{Code: "UNKNOWN_REFUSAL"}), "UNKNOWN_REFUSAL", "PROJECT_RESOLUTION_UNAVAILABLE"},
		{"known-logical-control", logical, "PROJECT_MIGRATION_REQUIRED", "PROJECT_MIGRATION_REQUIRED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			refusal := projectIdentityRefusal(test.err, cwd)
			require.NotNil(t, refusal)
			assert.Equal(t, test.code, refusal.Code, "retain the existing typed project error classification")
			assert.Contains(t, refusal.Message, test.code)
			guidance := projectidentity.RecoveryGuidance(test.guidanceCode)
			require.NotEmpty(t, guidance)
			assert.Contains(t, refusal.Message, guidance, "render only fixed supported recovery, never the native cause")
			assert.Contains(t, refusal.Message, fmt.Sprintf("caller_root_sha256=%x", sha256.Sum256([]byte(cwd))))
			assert.Contains(t, refusal.Message, "path withheld")
			assert.LessOrEqual(t, len(refusal.Message), 3072)
			assert.Empty(t, refusal.Details, "do not move a hidden cause/path into structured output")
			assert.Nil(t, refusal.RetryAfter)
			raw, err := json.Marshal(refusal)
			require.NoError(t, err)
			for _, rendered := range []string{refusal.Message, refusal.Error(), string(raw)} {
				for _, private := range []string{cwd, privatePath, "private-credential", "private-marker-body", "private-wrapper"} {
					assert.NotContains(t, rendered, private, "all caller-visible renderings must exclude raw I/O paths and wrapped-cause content")
				}
			}
		})
	}
}
