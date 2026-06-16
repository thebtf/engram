package embedding

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"gorm.io/gorm"
)

// vectorColumns are the (table, column) pairs that MUST carry vector(EmbeddingDim).
// Both subsystems share the unified dimension: memory chunks and code chunks.
var vectorColumns = []struct{ table, column string }{
	{"content_chunks", "embedding"},
	{"code_chunks", "embedding"},
}

// declaredTypeRe extracts N from a pgvector column type rendered by
// format_type, e.g. "vector(1536)" → 1536. halfvec(N) is matched too, so the
// assert also holds if a column is ever migrated to half precision at the same N.
var declaredTypeRe = regexp.MustCompile(`^(?:vector|halfvec)\((\d+)\)$`)

// AssertEmbeddingDimensions reconciles the live database against the SSOT
// EmbeddingDim. For each known vector column it reads the actual DECLARED type
// from the catalog and fails if its dimension does not equal EmbeddingDim.
//
// This converts the previously convention-only agreement between three
// representations of the dimension — the migration DDL literal, the GORM struct
// tag, and the EmbeddingDim constant — into a startup invariant. A drift (e.g. a
// new migration that forgets a column, or a GORM AutoMigrate that recreates a
// table at the wrong dimension) fails fast and loudly instead of silently
// corrupting search or rejecting INSERTs at runtime.
//
// A table/column that does not exist yet is SKIPPED (not an error): code_chunks
// only exists once its migration has run, and this assert may run on a fresh DB.
// Only a present column with the WRONG dimension is fatal.
//
// Dimension is read via format_type(atttypid, atttypmod), which renders the
// exact declared type string ("vector(1536)") — this round-trips the DDL and is
// correct regardless of pgvector's internal typmod encoding, so the check does
// not depend on any assumption about how the dimension is stored in the catalog.
// vector_dims() is unusable here: the table is empty immediately after the
// dimension migration, so there is no value to measure.
func AssertEmbeddingDimensions(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("embedding dim assert: db required")
	}
	for _, vc := range vectorColumns {
		var declType *string
		err := db.WithContext(ctx).Raw(`
			SELECT format_type(a.atttypid, a.atttypmod)
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public'
			  AND c.relname = ?
			  AND a.attname = ?
			  AND a.attnum > 0
			  AND NOT a.attisdropped
		`, vc.table, vc.column).Scan(&declType).Error
		if err != nil {
			return fmt.Errorf("embedding dim assert: query %s.%s: %w", vc.table, vc.column, err)
		}
		if declType == nil {
			// Column/table not present yet (fresh DB before its migration). Skip.
			continue
		}
		m := declaredTypeRe.FindStringSubmatch(*declType)
		if m == nil {
			return fmt.Errorf(
				"embedding dim assert: %s.%s has unexpected type %q — expected vector(%d)",
				vc.table, vc.column, *declType, EmbeddingDim,
			)
		}
		got, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			return fmt.Errorf("embedding dim assert: parse dimension from %q: %w", *declType, convErr)
		}
		if got != EmbeddingDim {
			return fmt.Errorf(
				"embedding dim assert: %s.%s is vector(%d) but EmbeddingDim is %d — schema/constant drift; "+
					"a migration must ALTER the column (or EmbeddingDim is wrong). Refusing to start the embedding path",
				vc.table, vc.column, got, EmbeddingDim,
			)
		}
	}
	return nil
}
