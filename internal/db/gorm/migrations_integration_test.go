package gorm

import (
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestMigrationsIntegration runs all migrations against a real PostgreSQL+pgvector instance.
// Requires DATABASE_DSN environment variable pointing to a test database.
//
//	DATABASE_DSN="postgres://user:pass@host:5432/db?sslmode=disable" go test ./internal/db/gorm/ -run TestMigrationsIntegration -v
func TestMigrationsIntegration(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	// Use 2000 dims — the target production configuration.
	const dims = 2000

	if err := runMigrations(db, dims); err != nil {
		t.Fatalf("runMigrations(dims=%d): %v", dims, err)
	}
	t.Logf("all migrations passed with dims=%d", dims)

	// Verify the embedding column has the expected dimension.
	var actual int
	row := db.Raw("SELECT atttypmod FROM pg_attribute WHERE attrelid = 'vectors'::regclass AND attname = 'embedding' AND atttypmod > 0").Row()
	if err := row.Scan(&actual); err != nil {
		t.Fatalf("read vector dimension: %v", err)
	}
	if actual != dims {
		t.Fatalf("vector dimension mismatch: got %d, want %d", actual, dims)
	}
	t.Logf("vectors.embedding = vector(%d) — correct", actual)
}
