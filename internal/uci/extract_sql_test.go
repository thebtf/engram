package uci

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const uciSQLDDLFixture = "-- Immutable lexical source: Café, 你好.\r\n" +
	"\r\n" +
	"CREATE TABLE public.accounts (\r\n" +
	"  id BIGINT NOT NULL,\r\n" +
	"  email TEXT NOT NULL,\r\n" +
	"  CONSTRAINT accounts_pkey PRIMARY KEY (id),\r\n" +
	"  CONSTRAINT accounts_email_key UNIQUE (email)\r\n" +
	");\r\n" +
	"\r\n" +
	"/* Observed DDL only; never a connection, execution, or schema claim. */\r\n" +
	"CREATE TABLE \"Billing\".\"Invoice\" (\r\n" +
	"  \"Invoice ID\" UUID NOT NULL,\r\n" +
	"  account_id BIGINT NOT NULL,\r\n" +
	"  CONSTRAINT \"Invoice Account FK\" FOREIGN KEY (account_id)\r\n" +
	"    REFERENCES public.accounts (id)\r\n" +
	");\r\n" +
	"\r\n" +
	"ALTER TABLE \"Billing\".\"Invoice\"\r\n" +
	"  ADD COLUMN \"Résumé\" VARCHAR(255) NOT NULL;\r\n"

func TestUCISQLExtractionProducesStableDDLFacts(t *testing.T) {
	source := []byte(uciSQLDDLFixture)
	original := append([]byte(nil), source...)
	profile := uciSQLExtractionProfile()

	first := ExtractSQL(source, profile)
	source[0] = 'X'
	second := ExtractSQL(append([]byte(nil), original...), profile)

	if first.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractSQL() coverage = %q, want %q", first.Coverage, IndexCoverageComplete)
	}
	if first.Text != string(original) {
		t.Fatalf("ExtractSQL() text = %q, want exact caller-owned CRLF/Unicode bytes", first.Text)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ExtractSQL() facts are not stable across identical byte/profile input:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireSQLExtractionProof(t, first)
	uciRequireSQLChunksReconstructText(t, first.Chunks, first.Text)

	publicAccounts := uciRequireSQLTable(t, first.Definitions, "table:public.accounts", uciSQLSpan(t, original, "CREATE TABLE public.accounts", 0))
	billingInvoice := uciRequireSQLTable(t, first.Definitions, "table:\"Billing\".\"Invoice\"", uciSQLSpan(t, original, "CREATE TABLE \"Billing\".\"Invoice\"", 0))
	if publicAccounts.SymbolKey == billingInvoice.SymbolKey {
		t.Fatalf("distinct table statements collapsed to one symbol key: public=%#v quoted=%#v", publicAccounts, billingInvoice)
	}

	uciRequireSQLColumn(t, first.Definitions, "column:public.accounts.id", "table:public.accounts", "BIGINT", uciSQLSpan(t, original, "id BIGINT NOT NULL", 0))
	uciRequireSQLColumn(t, first.Definitions, "column:public.accounts.email", "table:public.accounts", "TEXT", uciSQLSpan(t, original, "email TEXT NOT NULL", 0))
	uciRequireSQLConstraint(t, first.Definitions, "primary_key", "table:public.accounts", []string{"id"}, uciSQLSpan(t, original, "CONSTRAINT accounts_pkey PRIMARY KEY (id)", 0))
	uciRequireSQLConstraint(t, first.Definitions, "unique", "table:public.accounts", []string{"email"}, uciSQLSpan(t, original, "CONSTRAINT accounts_email_key UNIQUE (email)", 0))

	uciRequireSQLColumn(t, first.Definitions, "column:\"Billing\".\"Invoice\".\"Invoice ID\"", "table:\"Billing\".\"Invoice\"", "UUID", uciSQLSpan(t, original, "\"Invoice ID\" UUID NOT NULL", 0))
	uciRequireSQLColumn(t, first.Definitions, "column:\"Billing\".\"Invoice\".account_id", "table:\"Billing\".\"Invoice\"", "BIGINT", uciSQLSpan(t, original, "account_id BIGINT NOT NULL", 0))
	uciRequireSQLColumn(t, first.Definitions, "column:\"Billing\".\"Invoice\".\"Résumé\"", "table:\"Billing\".\"Invoice\"", "VARCHAR(255)", uciSQLSpan(t, original, "\"Résumé\" VARCHAR(255) NOT NULL", 0))
	uciRequireSQLReference(t, first.References, uciSQLReferenceExpectation{
		kind:           "foreign_key",
		ownerLocalKey:  "table:\"Billing\".\"Invoice\"",
		targetLocalKey: "table:public.accounts",
		columns:        []string{"account_id"},
		targetColumns:  []string{"id"},
		span:           uciSQLSpan(t, original, "REFERENCES public.accounts (id)", 0),
	})
}

func TestUCISQLExtractionSeparatesArtifactIdentityFromInputMutation(t *testing.T) {
	source := []byte(uciSQLDDLFixture)
	profile := uciSQLExtractionProfile()

	first := ExtractSQL(source, profile)
	reused := ExtractSQL(append([]byte(nil), source...), profile)
	uciRequireSQLExtractionProof(t, first)
	if !reflect.DeepEqual(first.Proof, reused.Proof) {
		t.Fatalf("identical SQL bytes/profile produced different artifact proof: first=%#v reused=%#v", first.Proof, reused.Proof)
	}

	changedProfile := profile
	changedProfile.ParserKey = "sql-ddl-lexer-v2"
	if changed := ExtractSQL(source, changedProfile); changed.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("parser-key change reused artifact identity %q", changed.Proof.ArtifactID)
	}

	changedBytes := append(append([]byte(nil), source...), []byte("-- byte change\r\n")...)
	if changed := ExtractSQL(changedBytes, profile); changed.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("source-byte change reused artifact identity %q", changed.Proof.ArtifactID)
	}
}

func TestUCISQLExtractionBoundsUnicodeChunksAcrossStatements(t *testing.T) {
	source := []byte("CREATE TABLE public.notes (body TEXT);\n/* " + strings.Repeat("α", 40<<10) + " */\nALTER TABLE public.notes ADD COLUMN title TEXT;\n")
	artifact := ExtractSQL(source, uciSQLExtractionProfile())

	if artifact.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractSQL(large DDL) coverage = %q, want %q", artifact.Coverage, IndexCoverageComplete)
	}
	if artifact.Text != string(source) {
		t.Fatal("ExtractSQL(large DDL) changed caller-owned Unicode bytes")
	}
	if len(artifact.Chunks) < 2 {
		t.Fatalf("ExtractSQL(large DDL) chunks = %d, want bounded multi-chunk output", len(artifact.Chunks))
	}
	uciRequireSQLExtractionProof(t, artifact)
	uciRequireSQLChunksReconstructText(t, artifact.Chunks, artifact.Text)
}

func TestUCISQLExtractionRetainsPartialFactsForMalformedDDL(t *testing.T) {
	source := []byte("CREATE TABLE public.kept (\n  id BIGINT NOT NULL\n);\nALTER TABLE public.kept ADD COLUMN broken VARCHAR(\n")
	artifact := ExtractSQL(source, uciSQLExtractionProfile())

	if artifact.Coverage != IndexCoveragePartial {
		t.Fatalf("ExtractSQL(malformed DDL) coverage = %q, want %q", artifact.Coverage, IndexCoveragePartial)
	}
	if artifact.Text != string(source) {
		t.Fatalf("ExtractSQL(malformed DDL) text = %q, want exact usable source", artifact.Text)
	}
	uciRequireSQLExtractionProof(t, artifact)
	uciRequireSQLDiagnostic(t, artifact.Diagnostics, "SQL_PARSE_ERROR")
	uciRequireSQLTable(t, artifact.Definitions, "table:public.kept", uciSQLSpan(t, source, "CREATE TABLE public.kept", 0))
}

func TestUCISQLExtractionRetainsCompletedStatementsBeforeMalformedSuffix(t *testing.T) {
	source := []byte("CREATE TABLE public.kept (id BIGINT NOT NULL);\nALTER TABLE public.kept ADD;")
	artifact := ExtractSQL(source, uciSQLExtractionProfile())

	if artifact.Coverage != IndexCoveragePartial {
		t.Fatalf("ExtractSQL(malformed suffix) coverage = %q, want %q", artifact.Coverage, IndexCoveragePartial)
	}
	uciRequireSQLExtractionProof(t, artifact)
	uciRequireSQLTable(t, artifact.Definitions, "table:public.kept", uciSQLSpan(t, source, "CREATE TABLE public.kept", 0))
	for _, diagnostic := range artifact.Diagnostics {
		if diagnostic.Code != "SQL_PARSE_ERROR" {
			continue
		}
		if got, want := artifact.Text[diagnostic.Span.ByteStart:diagnostic.Span.ByteEnd], "ALTER TABLE public.kept ADD;"; got != want {
			t.Fatalf("malformed suffix diagnostic covers %q, want %q", got, want)
		}
		return
	}
	t.Fatalf("malformed suffix diagnostics = %#v, want SQL_PARSE_ERROR", artifact.Diagnostics)
}

func TestUCISQLExtractionObservesButExcludesUnsupportedDML(t *testing.T) {
	source := []byte("CREATE TABLE public.events (id BIGINT);\nINSERT INTO public.events (id) VALUES (1);\nUPDATE public.events SET id = 2;\nDELETE FROM public.events;\n")
	artifact := ExtractSQL(source, uciSQLExtractionProfile())

	if artifact.Coverage != IndexCoveragePartial {
		t.Fatalf("ExtractSQL(DML) coverage = %q, want %q", artifact.Coverage, IndexCoveragePartial)
	}
	uciRequireSQLExtractionProof(t, artifact)
	uciRequireSQLDiagnostic(t, artifact.Diagnostics, "UNSUPPORTED_STATEMENT")
	uciRequireSQLTable(t, artifact.Definitions, "table:public.events", uciSQLSpan(t, source, "CREATE TABLE public.events", 0))
	for _, kind := range []string{"insert", "update", "delete"} {
		if uciHasSQLDefinitionKind(artifact.Definitions, kind) {
			t.Fatalf("unsupported DML %q was emitted as a SQL definition: %#v", kind, artifact.Definitions)
		}
	}
	if len(artifact.References) != 0 {
		t.Fatalf("unsupported DML emitted reference facts: %#v", artifact.References)
	}
}

func TestUCISQLExtractionHasPureByteBoundary(t *testing.T) {
	var _ func([]byte, SQLExtractionProfile) SQLArtifact = ExtractSQL

	uciRequireSQLStructFields(t, SQLExtractionProfile{}, []uciSQLField{
		{name: "ProfileKey", typ: reflect.TypeOf("")},
		{name: "ParserKey", typ: reflect.TypeOf("")},
	})
	uciRequireSQLStructFields(t, SQLArtifact{}, []uciSQLField{
		{name: "Proof", typ: reflect.TypeOf(IndexArtifactProof{})},
		{name: "Coverage", typ: reflect.TypeOf(IndexCoverageComplete)},
		{name: "Text", typ: reflect.TypeOf("")},
		{name: "Definitions", typ: reflect.TypeOf([]SQLDefinition(nil))},
		{name: "References", typ: reflect.TypeOf([]SQLReferenceSite(nil))},
		{name: "Chunks", typ: reflect.TypeOf([]SQLChunk(nil))},
		{name: "Diagnostics", typ: reflect.TypeOf([]SQLDiagnostic(nil))},
	})
	uciRequireSQLStructFields(t, SQLDefinition{}, []uciSQLField{
		{name: "Kind", typ: reflect.TypeOf("")},
		{name: "SymbolKey", typ: reflect.TypeOf("")},
		{name: "LocalKey", typ: reflect.TypeOf("")},
		{name: "OwnerLocalKey", typ: reflect.TypeOf("")},
		{name: "DataType", typ: reflect.TypeOf("")},
		{name: "Columns", typ: reflect.TypeOf([]string(nil))},
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
	})
	uciRequireSQLStructFields(t, SQLReferenceSite{}, []uciSQLField{
		{name: "Kind", typ: reflect.TypeOf("")},
		{name: "SymbolKey", typ: reflect.TypeOf("")},
		{name: "LocalKey", typ: reflect.TypeOf("")},
		{name: "OwnerLocalKey", typ: reflect.TypeOf("")},
		{name: "TargetKey", typ: reflect.TypeOf("")},
		{name: "TargetLocalKey", typ: reflect.TypeOf("")},
		{name: "Columns", typ: reflect.TypeOf([]string(nil))},
		{name: "TargetColumns", typ: reflect.TypeOf([]string(nil))},
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
	})
	uciRequireSQLStructFields(t, SQLChunk{}, []uciSQLField{
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
		{name: "Text", typ: reflect.TypeOf("")},
		{name: "ContentDigest", typ: reflect.TypeOf(IndexDigest(""))},
	})
	uciRequireSQLStructFields(t, SQLDiagnostic{}, []uciSQLField{
		{name: "Code", typ: reflect.TypeOf("")},
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
		{name: "Message", typ: reflect.TypeOf("")},
	})

	extractorType := reflect.TypeOf(ExtractSQL)
	if extractorType.NumIn() != 2 || extractorType.In(0) != reflect.TypeOf([]byte(nil)) || extractorType.In(1) != reflect.TypeOf(SQLExtractionProfile{}) || extractorType.NumOut() != 1 || extractorType.Out(0) != reflect.TypeOf(SQLArtifact{}) {
		t.Fatalf("ExtractSQL must remain func([]byte, SQLExtractionProfile) SQLArtifact, got %s", extractorType)
	}
	for _, value := range []any{SQLExtractionProfile{}, SQLArtifact{}, SQLDefinition{}, SQLReferenceSite{}, SQLChunk{}, SQLDiagnostic{}} {
		typ := reflect.TypeOf(value)
		if uciSQLTypeUsesDatabase(typ, map[reflect.Type]bool{}) {
			t.Fatalf("pure SQL extraction API leaks a database or driver type through %s", typ)
		}
		for index := range typ.NumMethod() {
			switch typ.Method(index).Name {
			case "Connect", "Exec", "Execute", "Open", "Query", "QueryRow":
				t.Fatalf("pure SQL extraction API exposes execution method %s on %s", typ.Method(index).Name, typ)
			}
		}
	}
}

type uciSQLField struct {
	name string
	typ  reflect.Type
}

func uciSQLExtractionProfile() SQLExtractionProfile {
	return SQLExtractionProfile{
		ProfileKey: "sql-ddl-structure-v1",
		ParserKey:  "sql-ddl-lexer-v1",
	}
}

func uciRequireSQLExtractionProof(t *testing.T, artifact SQLArtifact) {
	t.Helper()
	uciRequireSQLArtifactProofIdentity(t, artifact)
	uciRequireSQLArtifactChunks(t, artifact)
}

func uciRequireSQLArtifactProofIdentity(t *testing.T, artifact SQLArtifact) {
	t.Helper()
	if !canonicalContextUUID(artifact.Proof.ArtifactID) {
		t.Fatalf("artifact ID %q is not a canonical UUID", artifact.Proof.ArtifactID)
	}
	if !isIndexDigest(artifact.Proof.ContentDigest) {
		t.Fatalf("content digest %q is not a stable index digest", artifact.Proof.ContentDigest)
	}
	if !isIndexDigest(artifact.Proof.FactsDigest) {
		t.Fatalf("facts digest %q is not a stable index digest", artifact.Proof.FactsDigest)
	}
	if artifact.Proof.DefinitionCount != uint64(len(artifact.Definitions)) {
		t.Fatalf("definition count = %d, want %d", artifact.Proof.DefinitionCount, len(artifact.Definitions))
	}
	if artifact.Proof.ReferenceSiteCount != uint64(len(artifact.References)) {
		t.Fatalf("reference count = %d, want %d", artifact.Proof.ReferenceSiteCount, len(artifact.References))
	}
	if artifact.Proof.ChunkCount != uint64(len(artifact.Chunks)) {
		t.Fatalf("chunk count = %d, want %d", artifact.Proof.ChunkCount, len(artifact.Chunks))
	}
}

func uciRequireSQLArtifactChunks(t *testing.T, artifact SQLArtifact) {
	t.Helper()
	if len(artifact.Chunks) == 0 {
		t.Fatal("extraction returned no source chunks")
	}
	for index, chunk := range artifact.Chunks {
		if !isIndexDigest(chunk.ContentDigest) {
			t.Fatalf("chunk %d content digest %q is not a stable index digest", index, chunk.ContentDigest)
		}
		if chunk.Span.ByteStart < 0 || chunk.Span.ByteEnd <= chunk.Span.ByteStart || chunk.Span.ByteEnd > int64(len(artifact.Text)) {
			t.Fatalf("chunk %d has invalid byte span %#v for %d-byte text", index, chunk.Span, len(artifact.Text))
		}
		if chunk.Span.LineStart < 1 || chunk.Span.LineEnd < chunk.Span.LineStart {
			t.Fatalf("chunk %d has invalid inclusive line span %#v", index, chunk.Span)
		}
		if want := artifact.Text[chunk.Span.ByteStart:chunk.Span.ByteEnd]; chunk.Text != want {
			t.Fatalf("chunk %d text = %q, want text at span %q", index, chunk.Text, want)
		}
	}
}

func uciRequireSQLTable(t *testing.T, definitions []SQLDefinition, localKey string, requiredStart IndexSpan) SQLDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.Kind != "table" || definition.LocalKey != localKey {
			continue
		}
		if want := "sql:" + localKey; definition.SymbolKey != want {
			t.Fatalf("table %q symbol key = %q, want %q", localKey, definition.SymbolKey, want)
		}
		uciRequireSQLSpanContains(t, definition.Span, requiredStart, "table "+localKey)
		return definition
	}
	t.Fatalf("missing table definition %q; got %#v", localKey, definitions)
	return SQLDefinition{}
}

func uciRequireSQLColumn(t *testing.T, definitions []SQLDefinition, localKey, ownerLocalKey, dataType string, wantSpan IndexSpan) SQLDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.Kind != "column" || definition.LocalKey != localKey {
			continue
		}
		if want := "sql:" + localKey; definition.SymbolKey != want {
			t.Fatalf("column %q symbol key = %q, want %q", localKey, definition.SymbolKey, want)
		}
		if definition.OwnerLocalKey != ownerLocalKey {
			t.Fatalf("column %q owner = %q, want %q", localKey, definition.OwnerLocalKey, ownerLocalKey)
		}
		if definition.DataType != dataType {
			t.Fatalf("column %q data type = %q, want %q", localKey, definition.DataType, dataType)
		}
		uciRequireSQLSpan(t, definition.Span, wantSpan, "column "+localKey)
		return definition
	}
	t.Fatalf("missing column definition %q; got %#v", localKey, definitions)
	return SQLDefinition{}
}

func uciRequireSQLConstraint(t *testing.T, definitions []SQLDefinition, kind, ownerLocalKey string, columns []string, wantSpan IndexSpan) SQLDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.Kind != kind || definition.OwnerLocalKey != ownerLocalKey || !reflect.DeepEqual(definition.Columns, columns) {
			continue
		}
		if definition.SymbolKey == "" || definition.LocalKey == "" {
			t.Fatalf("%s constraint has no stable identity: %#v", kind, definition)
		}
		uciRequireSQLSpan(t, definition.Span, wantSpan, kind+" constraint")
		return definition
	}
	t.Fatalf("missing %s constraint for %q columns %q; got %#v", kind, ownerLocalKey, columns, definitions)
	return SQLDefinition{}
}

type uciSQLReferenceExpectation struct {
	kind           string
	ownerLocalKey  string
	targetLocalKey string
	columns        []string
	targetColumns  []string
	span           IndexSpan
}

func uciRequireSQLReference(t *testing.T, references []SQLReferenceSite, want uciSQLReferenceExpectation) SQLReferenceSite {
	t.Helper()
	for _, reference := range references {
		if reference.Kind != want.kind || reference.OwnerLocalKey != want.ownerLocalKey || reference.TargetLocalKey != want.targetLocalKey || !reflect.DeepEqual(reference.Columns, want.columns) || !reflect.DeepEqual(reference.TargetColumns, want.targetColumns) {
			continue
		}
		if reference.SymbolKey == "" || reference.LocalKey == "" {
			t.Fatalf("%s reference has no stable source identity: %#v", want.kind, reference)
		}
		if targetKey := "sql:" + want.targetLocalKey; reference.TargetKey != targetKey {
			t.Fatalf("%s reference target key = %q, want %q", want.kind, reference.TargetKey, targetKey)
		}
		uciRequireSQLSpan(t, reference.Span, want.span, want.kind+" reference")
		return reference
	}
	t.Fatalf("missing %s reference from %q to %q; got %#v", want.kind, want.ownerLocalKey, want.targetLocalKey, references)
	return SQLReferenceSite{}
}

func uciRequireSQLDiagnostic(t *testing.T, diagnostics []SQLDiagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %q; got %#v", code, diagnostics)
}

func uciRequireSQLChunksReconstructText(t *testing.T, chunks []SQLChunk, text string) {
	t.Helper()
	var rebuilt strings.Builder
	var nextStart int64
	for index, chunk := range chunks {
		if len(chunk.Text) > 64<<10 {
			t.Fatalf("chunk %d is %d bytes, exceeds the 64 KiB bound", index, len(chunk.Text))
		}
		if !utf8.ValidString(chunk.Text) {
			t.Fatalf("chunk %d splits a UTF-8 sequence: %q", index, chunk.Text)
		}
		if chunk.Span.ByteStart != nextStart {
			t.Fatalf("chunk %d starts at %d, want contiguous offset %d", index, chunk.Span.ByteStart, nextStart)
		}
		nextStart = chunk.Span.ByteEnd
		rebuilt.WriteString(chunk.Text)
	}
	if nextStart != int64(len(text)) {
		t.Fatalf("chunk coverage ends at %d, want retained text length %d", nextStart, len(text))
	}
	if rebuilt.String() != text {
		t.Fatal("bounded chunks do not losslessly reconstruct retained source text")
	}
}

func uciHasSQLDefinitionKind(definitions []SQLDefinition, want string) bool {
	for _, definition := range definitions {
		if definition.Kind == want {
			return true
		}
	}
	return false
}

func uciRequireSQLStructFields(t *testing.T, value any, want []uciSQLField) {
	t.Helper()
	typ := reflect.TypeOf(value)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s kind = %s, want struct", typ, typ.Kind())
	}
	if typ.NumField() != len(want) {
		t.Fatalf("%s fields = %d, want %d", typ, typ.NumField(), len(want))
	}
	for index, expected := range want {
		field := typ.Field(index)
		if field.Name != expected.name || field.Type != expected.typ {
			t.Fatalf("%s field %d = %s %s, want %s %s", typ, index, field.Name, field.Type, expected.name, expected.typ)
		}
	}
}

func uciSQLTypeUsesDatabase(typ reflect.Type, seen map[reflect.Type]bool) bool {
	if typ == nil || seen[typ] {
		return false
	}
	seen[typ] = true
	if typ.PkgPath() == "database/sql" || typ.PkgPath() == "database/sql/driver" || strings.HasPrefix(typ.PkgPath(), "gorm.io/") {
		return true
	}
	switch typ.Kind() {
	case reflect.Array, reflect.Pointer, reflect.Slice:
		return uciSQLTypeUsesDatabase(typ.Elem(), seen)
	case reflect.Map:
		return uciSQLTypeUsesDatabase(typ.Key(), seen) || uciSQLTypeUsesDatabase(typ.Elem(), seen)
	case reflect.Struct:
		return uciSQLStructUsesDatabase(typ, seen)
	case reflect.Func:
		return uciSQLFunctionUsesDatabase(typ, seen)
	}
	return false
}

func uciSQLStructUsesDatabase(typ reflect.Type, seen map[reflect.Type]bool) bool {
	for index := range typ.NumField() {
		if uciSQLTypeUsesDatabase(typ.Field(index).Type, seen) {
			return true
		}
	}
	return false
}

func uciSQLFunctionUsesDatabase(typ reflect.Type, seen map[reflect.Type]bool) bool {
	for index := range typ.NumIn() {
		if uciSQLTypeUsesDatabase(typ.In(index), seen) {
			return true
		}
	}
	for index := range typ.NumOut() {
		if uciSQLTypeUsesDatabase(typ.Out(index), seen) {
			return true
		}
	}
	return false
}

func uciRequireSQLSpan(t *testing.T, got, want IndexSpan, subject string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s span = %#v, want %#v", subject, got, want)
	}
}

func uciRequireSQLSpanContains(t *testing.T, got, required IndexSpan, subject string) {
	t.Helper()
	if got.ByteStart > required.ByteStart || got.ByteEnd < required.ByteEnd || got.LineStart != required.LineStart {
		t.Fatalf("%s span %#v does not cover required source anchor %#v", subject, got, required)
	}
}

func uciSQLSpan(t *testing.T, source []byte, fragment string, occurrence int) IndexSpan {
	t.Helper()
	searchFrom := 0
	for found := 0; ; found++ {
		offset := bytes.Index(source[searchFrom:], []byte(fragment))
		if offset < 0 {
			t.Fatalf("fragment %q occurrence %d not found in fixture", fragment, occurrence)
		}
		offset += searchFrom
		if found == occurrence {
			end := offset + len(fragment)
			lineEndOffset := offset
			if end > offset {
				lineEndOffset = end - 1
			}
			return IndexSpan{
				ByteStart: int64(offset),
				ByteEnd:   int64(end),
				LineStart: bytes.Count(source[:offset], []byte("\n")) + 1,
				LineEnd:   bytes.Count(source[:lineEndOffset], []byte("\n")) + 1,
			}
		}
		searchFrom = offset + len(fragment)
	}
}
