package recoveryinventory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
)

// ScanSource inventories source structure without returning source content.
func ScanSource(root string) (Report, error) {
	report := newReport("current-source")
	files, err := sourceFiles(root, ".go", ".js", ".mjs", ".ts", ".tsx", ".vue", ".proto", ".json", ".yaml", ".yml", ".md")
	if err != nil {
		return Report{}, err
	}

	for _, file := range files {
		if filepath.Ext(file.relative) != ".go" {
			report.add(Record{Kind: "source-file", Path: file.relative, Classification: "source-declared"})
			continue
		}
		if err := scanGoSourceFile(&report, file); err != nil {
			return Report{}, err
		}
	}
	report.finish()
	return report, nil
}

func scanGoSourceFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.relative, source, 0)
	if err != nil {
		report.add(Record{Kind: "go-source", Path: file.relative, Classification: "source-uncertain"})
		return nil
	}
	report.add(Record{Kind: "go-package", Path: file.relative, Line: fset.Position(parsed.Package).Line, Name: parsed.Name.Name, Classification: "source-declared"})

	for _, declaration := range parsed.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			kind := "go-function"
			if declaration.Recv != nil {
				kind = "go-method"
			}
			report.add(Record{Kind: kind, Path: file.relative, Line: fset.Position(declaration.Pos()).Line, Name: declaration.Name.Name, Classification: "source-declared"})
		case *ast.GenDecl:
			for _, spec := range declaration.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				report.add(Record{Kind: "go-type", Path: file.relative, Line: fset.Position(typeSpec.Pos()).Line, Name: typeSpec.Name.Name, Classification: "source-declared"})
			}
		}
	}
	return nil
}
