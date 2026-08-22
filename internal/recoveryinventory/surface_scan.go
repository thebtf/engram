package recoveryinventory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	toolNamePattern     = regexp.MustCompile(`(?:Name|name)\s*:\s*"([A-Za-z][A-Za-z0-9_.-]*)"`)
	openClawToolPattern = regexp.MustCompile(`\bname\s*:\s*['"]([A-Za-z][A-Za-z0-9_.-]*)['"]`)
	rpcPattern          = regexp.MustCompile(`(?m)^\s*rpc\s+([A-Za-z][A-Za-z0-9_]*)\s*\(`)
)

// ScanSurfaces inventories source-declared transports and claims. UI roots are
// claim-only: neither their behavior nor their consumers are inferred.
func ScanSurfaces(root string) (Report, error) {
	report := newReport("package-route-hook-tool-documentation-claims")
	for _, surfaceRoot := range []string{"ui", "apps/operator-console"} {
		classification := "claim-only"
		if _, err := os.Stat(filepath.Join(root, surfaceRoot)); os.IsNotExist(err) {
			classification = "source-uncertain"
		}
		report.add(Record{Kind: "ui-root-claim", Path: filepath.ToSlash(surfaceRoot), Name: surfaceRoot, Classification: classification, ClaimOnly: true})
	}

	files, err := sourceFiles(root, ".go", ".js", ".mjs", ".ts", ".tsx", ".vue", ".proto", ".md")
	if err != nil {
		return Report{}, err
	}
	for _, file := range files {
		if err := scanSurfaceFile(&report, file); err != nil {
			return Report{}, err
		}
	}
	report.finish()
	return report, nil
}

func scanSurfaceFile(report *Report, file sourceFile) error {
	path := file.relative
	lowerPath := strings.ToLower(path)
	if strings.HasPrefix(path, "ui/") || strings.HasPrefix(path, "apps/operator-console/") {
		report.add(Record{Kind: "ui-claim", Path: path, Classification: "claim-only", ClaimOnly: true})
		return nil
	}
	if strings.HasSuffix(lowerPath, ".md") {
		report.add(Record{Kind: "current-documentation-claim", Path: path, Classification: "source-declared", ClaimOnly: true})
		return nil
	}
	if strings.Contains(path, "/hooks/") || strings.HasPrefix(path, "plugin/engram/hooks/") {
		report.add(Record{Kind: "hook", Path: path, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Classification: "source-declared"})
	}
	if strings.HasPrefix(path, "internal/mcp/") {
		report.add(Record{Kind: "mcp-surface", Path: path, Classification: "source-declared"})
	}
	if strings.HasPrefix(path, "cmd/engram/") || strings.HasPrefix(path, "internal/handlers/") {
		report.add(Record{Kind: "daemon-surface", Path: path, Classification: "source-declared"})
	}

	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	if filepath.Ext(path) == ".go" {
		scanGoRoutes(report, path, source)
	}
	for line, text := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(path, "internal/mcp/") {
			for _, match := range toolNamePattern.FindAllStringSubmatch(text, -1) {
				report.add(Record{Kind: "mcp-tool", Path: path, Line: line + 1, Name: redactedName(match[1]), Classification: "source-declared"})
			}
		}
		if strings.HasPrefix(path, "plugin/openclaw-engram/src/tools/") {
			for _, match := range openClawToolPattern.FindAllStringSubmatch(text, -1) {
				report.add(Record{Kind: "openclaw-tool", Path: path, Line: line + 1, Name: redactedName(match[1]), Classification: "source-declared"})
			}
		}
		for _, match := range rpcPattern.FindAllStringSubmatch(text, -1) {
			report.add(Record{Kind: "grpc-method", Path: path, Line: line + 1, Name: match[1], Classification: "source-declared"})
		}
	}
	return nil
}

func scanGoRoutes(report *Report, path string, source []byte) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		report.add(Record{Kind: "http-route", Path: path, Classification: "source-uncertain"})
		return
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Body != nil {
			scanRouteNode(report, fset, path, function.Body, "")
		}
	}
}

func scanRouteNode(report *Report, fset *token.FileSet, path string, node ast.Node, prefix string) {
	ast.Inspect(node, func(current ast.Node) bool {
		call, ok := current.(*ast.CallExpr)
		if !ok {
			return true
		}
		if routePrefix, callback, ok := routeCallback(call); ok {
			scanRouteNode(report, fset, path, callback.Body, joinRoute(prefix, routePrefix))
			return false
		}
		if callback, ok := routeGroupCallback(call); ok {
			scanRouteNode(report, fset, path, callback.Body, prefix)
			return false
		}
		if method, route, ok := routeEndpoint(call); ok {
			report.add(Record{Kind: "http-route", Path: path, Line: fset.Position(call.Pos()).Line, Name: method + " " + joinRoute(prefix, route), Classification: "source-declared"})
			return false
		}
		return true
	})
}

func routeCallback(call *ast.CallExpr) (string, *ast.FuncLit, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Route" || len(call.Args) != 2 {
		return "", nil, false
	}
	route, ok := stringLiteral(call.Args[0])
	if !ok {
		return "", nil, false
	}
	callback, ok := call.Args[1].(*ast.FuncLit)
	return route, callback, ok
}

func routeGroupCallback(call *ast.CallExpr) (*ast.FuncLit, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Group" || len(call.Args) != 1 {
		return nil, false
	}
	callback, ok := call.Args[0].(*ast.FuncLit)
	return callback, ok
}

func routeEndpoint(call *ast.CallExpr) (string, string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	if selector.Sel.Name == "Get" && isQueryGetter(selector.X) {
		return "", "", false
	}
	switch selector.Sel.Name {
	case "Get", "Post", "Put", "Patch", "Delete", "Head", "Options":
		if len(call.Args) == 0 {
			return "", "", false
		}
		route, ok := stringLiteral(call.Args[0])
		return strings.ToUpper(selector.Sel.Name), route, ok
	case "Method":
		if len(call.Args) < 2 {
			return "", "", false
		}
		method, ok := httpMethod(call.Args[0])
		if !ok {
			return "", "", false
		}
		route, ok := stringLiteral(call.Args[1])
		return method, route, ok
	default:
		return "", "", false
	}
}

func isQueryGetter(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Query"
}

func httpMethod(expression ast.Expr) (string, bool) {
	if method, ok := stringLiteral(expression); ok {
		return strings.ToUpper(method), true
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if packageName, ok := selector.X.(*ast.Ident); !ok || packageName.Name != "http" || !strings.HasPrefix(selector.Sel.Name, "Method") {
		return "", false
	}
	return strings.TrimPrefix(selector.Sel.Name, "Method"), true
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func joinRoute(prefix, route string) string {
	if prefix == "" {
		if route == "" {
			return "/"
		}
		if strings.HasPrefix(route, "/") {
			return route
		}
		return "/" + route
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(route, "/")
}
