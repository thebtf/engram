package pgvector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/vector"
)

func TestBuildWhereClauses_WithFilePathFilter(t *testing.T) {
	where := vector.BuildWhereFilter(vector.DocTypeObservation, "project-alpha", true, []string{"foo.go", "bar.go"})

	clauses, args, err := buildWhereClauses(where, 2)
	require.NoError(t, err)

	query := buildWhereClause(clauses)

	assert.Contains(t, query, "files_modified ?| $")
	assert.Contains(t, query, "files_read ?| $")
	assert.Contains(t, query, "(files_modified ?| $")
	assert.Contains(t, query, "files_read ?| $")

	// Ensure the JSONB any array is parameterized and not interpolated into SQL.
	assert.NotContains(t, query, "foo.go")
	assert.NotContains(t, query, "bar.go")

	// file path filter should use one parameter for both JSONB any checks.
	assert.Len(t, args, 4)
	if assert.Equal(t, 4, len(args)) {
		assert.Equal(t, "observation", args[0])
		assert.Equal(t, "project-alpha", args[1])
		assert.Equal(t, "global", args[2])
		assert.Equal(t, []string{"foo.go", "bar.go"}, args[3])
	}

	assert.Contains(t, query, "doc_type = $2")
	assert.Contains(t, query, "project = $3")
	assert.Contains(t, query, "scope = $4")
	assert.Contains(t, query, "files_modified ?| $5")
	assert.Contains(t, query, "files_read ?| $5")
}

func TestBuildWhereClauses_WithoutFilePathsDoesNotAddCondition(t *testing.T) {
	where := vector.BuildWhereFilter(vector.DocTypeObservation, "project-alpha", false, nil)

	clauses, args, err := buildWhereClauses(where, 2)
	require.NoError(t, err)

	query := buildWhereClause(clauses)
	assert.NotContains(t, query, "files_modified")
	assert.NotContains(t, query, "files_read")
	assert.NotContains(t, query, "?|")
	assert.Len(t, args, 2)
	assert.Equal(t, "observation", args[0])
	assert.Equal(t, "project-alpha", args[1])
}
