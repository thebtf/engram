package recoveryinventory

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	shortRoutePattern  = regexp.MustCompile(`\.(Get|Post|Put|Patch|Delete)\(\s*"([^"]+)"`)
	methodRoutePattern = regexp.MustCompile(`\.Method\(\s*http\.Method([A-Za-z]+)\s*,\s*"([^"]+)"`)
	toolNamePattern    = regexp.MustCompile(`(?:Name|name)\s*:\s*"([A-Za-z][A-Za-z0-9_.-]*)"`)
	rpcPattern         = regexp.MustCompile(`(?m)^\s*rpc\s+([A-Za-z][A-Za-z0-9_]*)\s*\(`)
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
	for line, text := range strings.Split(string(source), "\n") {
		for _, match := range shortRoutePattern.FindAllStringSubmatch(text, -1) {
			report.add(Record{Kind: "http-route", Path: path, Line: line + 1, Name: strings.ToUpper(match[1]) + " " + match[2], Classification: "source-declared"})
		}
		for _, match := range methodRoutePattern.FindAllStringSubmatch(text, -1) {
			report.add(Record{Kind: "http-route", Path: path, Line: line + 1, Name: strings.ToUpper(match[1]) + " " + match[2], Classification: "source-declared"})
		}
		if strings.HasPrefix(path, "internal/mcp/") {
			for _, match := range toolNamePattern.FindAllStringSubmatch(text, -1) {
				report.add(Record{Kind: "mcp-tool", Path: path, Line: line + 1, Name: redactedName(match[1]), Classification: "source-declared"})
			}
		}
		for _, match := range rpcPattern.FindAllStringSubmatch(text, -1) {
			report.add(Record{Kind: "grpc-method", Path: path, Line: line + 1, Name: match[1], Classification: "source-declared"})
		}
	}
	return nil
}
