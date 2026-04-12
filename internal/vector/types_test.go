package vector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWhereFilter_WithFilePaths(t *testing.T) {
	filter := BuildWhereFilter(DocTypeObservation, "project-alpha", true, []string{"foo.go"})

	assert.Len(t, filter.Clauses, 3)

	docTypeClause := filter.Clauses[0]
	assert.Equal(t, "doc_type", docTypeClause.Column)
	assert.Equal(t, "=", docTypeClause.Operator)
	assert.Equal(t, string(DocTypeObservation), docTypeClause.Value)

	scopeGroup := filter.Clauses[1]
	require.Len(t, scopeGroup.OrGroup, 2)
	assert.Equal(t, "project", scopeGroup.OrGroup[0].Column)
	assert.Equal(t, "=", scopeGroup.OrGroup[0].Operator)
	assert.Equal(t, "=", scopeGroup.OrGroup[1].Operator)

	filePathGroup := filter.Clauses[2]
	require.Len(t, filePathGroup.OrGroup, 2)
	assert.Equal(t, "files_modified", filePathGroup.OrGroup[0].Column)
	assert.Equal(t, "?|", filePathGroup.OrGroup[0].Operator)
	assert.Equal(t, []string{"foo.go"}, filePathGroup.OrGroup[0].Value)
	assert.Equal(t, "files_read", filePathGroup.OrGroup[1].Column)
	assert.Equal(t, "?|", filePathGroup.OrGroup[1].Operator)
	assert.Equal(t, []string{"foo.go"}, filePathGroup.OrGroup[1].Value)
}

func TestBuildWhereFilter_WithoutFilePathsDoesNotAddFileScopeClause(t *testing.T) {
	filter := BuildWhereFilter(DocTypeObservation, "project-alpha", false, nil)

	for _, clause := range filter.Clauses {
		assert.NotEqual(t, "files_modified", clause.Column)
		assert.NotEqual(t, "files_read", clause.Column)
	}

	hasFileFilter := false
	for _, clause := range filter.Clauses {
		if clause.Column == "files_modified" || clause.Column == "files_read" {
			hasFileFilter = true
			break
		}
		for _, orClause := range clause.OrGroup {
			if orClause.Column == "files_modified" || orClause.Column == "files_read" {
				hasFileFilter = true
				break
			}
		}
	}
	assert.False(t, hasFileFilter)
}
