package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/thebtf/engram/tools/gen-data-model/datamodel"
)

func main() {
	migrationsPath := flag.String("migrations", "internal/db/gorm/migrations.go", "path to migrations.go")
	docPath := flag.String("doc", "docs/arch/DATA_MODEL.md", "path to DATA_MODEL.md")
	check := flag.Bool("check", false, "print the generated block without modifying DATA_MODEL.md")
	flag.Parse()

	derivation, err := datamodel.DeriveFromMigrationsPackage(*migrationsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "derive data model: %v\n", err)
		os.Exit(1)
	}

	if *check {
		fmt.Print(derivation.Block)
		return
	}

	doc, err := os.ReadFile(*docPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *docPath, err)
		os.Exit(1)
	}
	updated := datamodel.SpliceGeneratedBlock(string(doc), derivation.Block)
	if err := os.WriteFile(*docPath, []byte(updated), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *docPath, err)
		os.Exit(1)
	}
}
