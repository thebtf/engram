package recoveryinventory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var vueScriptBlockPattern = regexp.MustCompile(`(?is)<script(?:\s+[^>]*)?>(.*?)</script\s*>`)

const projectDataKind = "project-bearing-data"

// ScanProjectData inventories source-declared project-bearing data families.
// It never opens a database, cache, import, export, or job payload.
func ScanProjectData(root string) (Report, error) {
	report := newReport(projectDataKind)
	files, err := sourceFiles(root, ".go", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".vue")
	if err != nil {
		return Report{}, err
	}
	for _, file := range files {
		if isTestSource(file.relative) {
			continue
		}
		var scanErr error
		switch filepath.Ext(file.relative) {
		case ".go":
			scanErr = scanProjectDataFile(&report, file)
		case ".vue":
			scanErr = scanVueProjectDataFile(&report, file)
		default:
			scanErr = scanJavaScriptProjectDataFile(&report, file)
		}
		if scanErr != nil {
			return Report{}, scanErr
		}
	}
	report.finish()
	return report, nil
}

func scanProjectDataFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.relative, source, 0)
	if err != nil {
		report.add(Record{Kind: "project-data-family", Path: file.relative, Classification: classificationSourceUncertain})
		return nil
	}

	found := false
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if !projectBearingName(typeSpec.Name.Name) && !projectBearingName(name.Name) && !projectBearingTag(field.Tag) {
						continue
					}
					found = true
					report.add(Record{
						Kind:           "project-bearing-field",
						Path:           file.relative,
						Line:           fset.Position(field.Pos()).Line,
						Name:           redactedName(typeSpec.Name.Name + "." + name.Name),
						Classification: projectDataClassification(file.relative, typeSpec.Name.Name, field),
					})
				}
			}
		}
	}
	if scanProjectBearingGoMapLiterals(report, file.relative, fset, parsed) {
		found = true
	}
	if !found && projectBearingGoCode(parsed) {
		report.add(Record{Kind: "project-data-family", Path: file.relative, Name: "unresolved-declaration", Classification: classificationSourceUncertain})
	}
	return nil
}

func projectBearingGoCode(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && projectBearingName(identifier.Name) {
			found = true
		}
		return !found
	})
	return found
}

func scanProjectBearingGoMapLiterals(report *Report, path string, fset *token.FileSet, file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if _, ok := literal.Type.(*ast.MapType); !ok {
			return true
		}
		for _, element := range literal.Elts {
			entry, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := entry.Key.(*ast.BasicLit)
			if !ok || key.Kind != token.STRING || !projectBearingTag(key) {
				continue
			}
			found = true
			report.add(Record{
				Kind:           projectDataKind,
				Path:           path,
				Line:           fset.Position(key.Pos()).Line,
				Classification: "serialized",
			})
		}
		return true
	})
	return found
}

func scanJavaScriptProjectDataFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	scanJavaScriptProjectData(report, file, string(source), 0)
	return nil
}

func scanVueProjectDataFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	sourceText := string(source)
	for _, block := range vueScriptBlockPattern.FindAllStringSubmatchIndex(sourceText, -1) {
		scriptStart, scriptEnd := block[2], block[3]
		scanJavaScriptProjectData(report, file, sourceText[scriptStart:scriptEnd], strings.Count(sourceText[:scriptStart], "\n"))
	}
	return nil
}

func scanJavaScriptProjectData(report *Report, file sourceFile, source string, lineOffset int) {
	var state javaScriptLexState
	var templateState javaScriptTemplateProjectState
	for index, line := range strings.Split(source, "\n") {
		code := javaScriptProjectCodeMask(line, &state)
		if !projectBearingJavaScriptCode(code) && !templateState.hasProjectContext(line) {
			continue
		}
		report.add(Record{Kind: projectDataKind, Path: file.relative, Line: lineOffset + index + 1, Classification: classificationSourceUncertain})
	}
}

// javaScriptTemplateProjectState ignores literal template text while retaining
// project-bearing expressions interpolated into a template literal.
type javaScriptTemplateProjectState struct {
	blockComment bool
	quote        byte
	modes        []javaScriptTemplateProjectMode
}

type javaScriptTemplateProjectMode struct {
	interpolation bool
	braces        int
}

func (state *javaScriptTemplateProjectState) hasProjectContext(line string) bool {
	found := false
	for index := 0; index < len(line); {
		if state.blockComment {
			end := strings.Index(line[index:], "*/")
			if end < 0 {
				return found
			}
			state.blockComment = false
			index += end + len("*/")
			continue
		}
		if state.quote != 0 {
			end, closed := quotedStringEnd(line, index, state.quote)
			if !closed {
				if !javaScriptContinuesString(line) {
					state.quote = 0
				}
				return found
			}
			state.quote = 0
			index = end
			continue
		}

		if state.inTemplate() {
			switch line[index] {
			case '\\':
				index += 2
			case '`':
				state.modes = state.modes[:len(state.modes)-1]
				index++
			case '$':
				if index+1 < len(line) && line[index+1] == '{' {
					state.modes = append(state.modes, javaScriptTemplateProjectMode{interpolation: true, braces: 1})
					index += 2
					continue
				}
				index++
			default:
				index++
			}
			continue
		}

		if line[index] == '/' && index+1 < len(line) {
			switch line[index+1] {
			case '/':
				return found
			case '*':
				state.blockComment = true
				index += 2
				continue
			}
		}
		switch line[index] {
		case '\'', '"':
			state.quote = line[index]
			index++
		case '`':
			state.modes = append(state.modes, javaScriptTemplateProjectMode{})
			index++
		case '{':
			if state.inInterpolation() {
				state.modes[len(state.modes)-1].braces++
			}
			index++
		case '}':
			if state.inInterpolation() {
				state.modes[len(state.modes)-1].braces--
				if state.modes[len(state.modes)-1].braces == 0 {
					state.modes = state.modes[:len(state.modes)-1]
				}
			}
			index++
		default:
			end := index
			for end < len(line) && (line[end] == '$' || line[end] == '_' || line[end] >= '0' && line[end] <= '9' || line[end] >= 'A' && line[end] <= 'Z' || line[end] >= 'a' && line[end] <= 'z') {
				end++
			}
			if state.inInterpolation() && end > index && projectBearingName(line[index:end]) {
				found = true
			}
			if end == index {
				index++
			} else {
				index = end
			}
		}
	}
	return found
}

func (state *javaScriptTemplateProjectState) inTemplate() bool {
	return len(state.modes) > 0 && !state.modes[len(state.modes)-1].interpolation
}

func (state *javaScriptTemplateProjectState) inInterpolation() bool {
	return len(state.modes) > 0 && state.modes[len(state.modes)-1].interpolation
}

func javaScriptProjectCodeMask(line string, state *javaScriptLexState) string {
	code, _ := javaScriptCodeMask(line, state)
	masked := []byte(code)
	for index := range masked {
		if masked[index] != '\'' && masked[index] != '"' {
			continue
		}
		end := javaScriptStringEnd(code, index)
		blankBytes(masked, index, end)
		index = end - 1
	}
	return string(masked)
}

func projectBearingJavaScriptCode(code string) bool {
	lower := strings.ToLower(code)
	return strings.Contains(lower, "project") || strings.Contains(lower, "tenant") || strings.Contains(lower, "workspace")
}

func projectBearingName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "project") || strings.Contains(lower, "tenant") || strings.Contains(lower, "workspace")
}

func projectBearingTag(tag *ast.BasicLit) bool {
	if tag == nil {
		return false
	}
	lower := strings.ToLower(tag.Value)
	return strings.Contains(lower, "project") || strings.Contains(lower, "tenant") || strings.Contains(lower, "workspace")
}

func projectDataClassification(path, typeName string, field *ast.Field) string {
	pathKind := projectDataPath(path + " " + typeName)
	if pathKind != "" {
		return pathKind
	}
	if field.Tag != nil {
		tag := strings.ToLower(field.Tag.Value)
		if strings.Contains(tag, "gorm:") {
			if len(field.Names) == 1 && strings.HasSuffix(strings.ToLower(field.Names[0].Name), "s") {
				return "relational-multi-role"
			}
			return "relational"
		}
		if strings.Contains(tag, "json:") || strings.Contains(tag, "yaml:") {
			return "serialized"
		}
	}
	return classificationSourceUncertain
}

func projectDataPath(value string) string {
	lower := strings.ToLower(filepath.ToSlash(value))
	switch {
	case strings.Contains(lower, "cache"):
		return "cache"
	case strings.Contains(lower, "job"), strings.Contains(lower, "queue"), strings.Contains(lower, "task"):
		return "job"
	case strings.Contains(lower, "import"), strings.Contains(lower, "export"):
		return "import-export"
	default:
		return ""
	}
}
