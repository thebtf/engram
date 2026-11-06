package worker

import (
	"testing"

	"github.com/stretchr/testify/require"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	gormlib "gorm.io/gorm"
)

func taskContextCatalogSnapshot(t *testing.T, database *gormlib.DB) map[string]string {
	t.Helper()
	models := []interface{ TableName() string }{gormstore.UCISource{}, gormstore.UCICheckout{}, gormstore.UCIAnalysisProfile{}, gormstore.UCIView{}, gormstore.UCIJob{}, gormstore.UCIParseArtifact{}, gormstore.UCIMembership{}, gormstore.UCIResolvedEdge{}}
	snapshot := make(map[string]string, len(models))
	for _, model := range models {
		var digest string
		table := model.TableName()
		require.NoError(t, database.Raw("SELECT md5(COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'')) FROM "+table+" AS row").Scan(&digest).Error)
		snapshot[table] = digest
	}
	return snapshot
}
