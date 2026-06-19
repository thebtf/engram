package grpcserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionStartRuleRouterHotPathDoesNotImportForbiddenPipelines(t *testing.T) {
	t.Parallel()

	for _, rel := range []string{
		"session_start.go",
		filepath.Join("..", "ruleinjection", "router.go"),
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			file := parseGoFileForHotPathGuard(t, rel)
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, forbidden := range []string{
					"/internal/llm",
					"/internal/embedding",
					"/internal/graph",
					"/internal/reranking",
					"/internal/retrieval",
				} {
					if strings.Contains(path, forbidden) {
						t.Fatalf("%s imports forbidden hot-path dependency %q", rel, path)
					}
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				switch strings.ToLower(ident.Name) {
				case "retrieverelevant", "rerank", "crossencoder", "embed", "llm":
					t.Fatalf("%s references forbidden hot-path symbol %q", rel, ident.Name)
				}
				return true
			})
		})
	}
}

func parseGoFileForHotPathGuard(t *testing.T, rel string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), rel, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return file
}
