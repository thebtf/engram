package uci

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/projectidentity"
)

func TestUCIScannerScansAuthorizedOrdinaryAndLinkedWorktrees(t *testing.T) {
	for _, test := range []struct {
		name   string
		linked bool
	}{
		{name: "ordinary worktree"},
		{name: "linked worktree git file", linked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScannerFixture(t, test.linked)
			body := []byte("package main\n\nfunc main() {}\n")
			fixture.write(t, "cmd/hello.go", body)
			fixture.git.tracked = []scannerGitPath{{Mode: "100644", Path: "cmd/hello.go"}}

			result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
			if err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
			scannerAssertCensus(t, result, IndexScanComplete, true, true)
			scannerAssertFile(t, result, "cmd/hello.go", IndexFilePresent, ScannerExclusionNone, body)
			scannerAssertOptionalString(t, result.Observation.HeadOID, fixture.git.headOID, fixture.git.hasHeadOID, "observation.head_oid")
			scannerAssertOptionalString(t, result.Observation.ObjectFormat, fixture.git.objectFormat, true, "observation.object_format")
			scannerAssertOptionalString(t, result.Observation.RefLabel, fixture.git.refLabel, fixture.git.hasRefLabel, "observation.ref_label")
			scannerAssertGitPlumbing(t, fixture.git, fixture.root)

			gitEntry, statErr := os.Stat(filepath.Join(fixture.root, ".git"))
			if statErr != nil {
				t.Fatalf("stat fixture .git: %v", statErr)
			}
			if gotLinked := !gitEntry.IsDir(); gotLinked != test.linked {
				t.Fatalf("fixture .git linked-file state = %t, want %t", gotLinked, test.linked)
			}
		})
	}
}

func TestUCIScannerExcludesTrackedProjectAnchorFromCodeAdmission(t *testing.T) {
	root := t.TempDir()
	if err := exec.CommandContext(t.Context(), "git", "init", root).Run(); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	anchor := []byte(`{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"scanner-anchor","scope":"repository"}`)
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), anchor, 0o600); err != nil {
		t.Fatalf("write project anchor: %v", err)
	}
	goSource := []byte("package fixture\n\nfunc AnchorBoundary() string { return \"ok\" }\n")
	if err := os.WriteFile(filepath.Join(root, "fixture.go"), goSource, 0o600); err != nil {
		t.Fatalf("write Go source: %v", err)
	}
	if err := exec.CommandContext(t.Context(), "git", "-C", root, "add", "--", ".engram-project", "fixture.go").Run(); err != nil {
		t.Fatalf("track repository fixture: %v", err)
	}
	if _, err := projectidentity.DiscoverAnchorV3(root, "repository"); err != nil {
		t.Fatalf("resolve tracked V3 anchor: %v", err)
	}
	result, err := NewScanner(ExecGitRunner{}, OSScannerFileSystem{}, ScannerPolicy{}).Scan(t.Context(), AuthorizedRootEvidence{RootPath: root})
	if err != nil {
		t.Fatalf("scan anchored repository: %v", err)
	}
	scannerAssertCensus(t, result, IndexScanComplete, true, true)
	scannerAssertFile(t, result, "fixture.go", IndexFilePresent, ScannerExclusionNone, goSource)
	scannerAssertFileAbsent(t, result, ".engram-project")
}

func TestUCIScannerSupportsDetachedUnbornAndObjectFormatStates(t *testing.T) {
	sha1 := strings.Repeat("a", 40)
	sha256 := strings.Repeat("b", 64)

	for _, test := range []struct {
		name         string
		objectFormat string
		headOID      string
		hasHeadOID   bool
		refLabel     string
		hasRefLabel  bool
	}{
		{name: "detached sha1", objectFormat: "sha1", headOID: sha1, hasHeadOID: true},
		{name: "detached sha256", objectFormat: "sha256", headOID: sha256, hasHeadOID: true},
		{name: "unborn head", objectFormat: "sha1", refLabel: "main", hasRefLabel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScannerFixture(t, false)
			body := []byte("package saved\n")
			fixture.write(t, "saved.go", body)
			fixture.git.objectFormat = test.objectFormat
			fixture.git.headOID = test.headOID
			fixture.git.hasHeadOID = test.hasHeadOID
			fixture.git.refLabel = test.refLabel
			fixture.git.hasRefLabel = test.hasRefLabel
			fixture.git.tracked = []scannerGitPath{{Mode: "100644", Path: "saved.go"}}

			result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
			if err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
			scannerAssertCensus(t, result, IndexScanComplete, true, true)
			scannerAssertFile(t, result, "saved.go", IndexFilePresent, ScannerExclusionNone, body)
			scannerAssertOptionalString(t, result.Observation.HeadOID, test.headOID, test.hasHeadOID, "observation.head_oid")
			scannerAssertOptionalString(t, result.Observation.ObjectFormat, test.objectFormat, true, "observation.object_format")
			scannerAssertOptionalString(t, result.Observation.RefLabel, test.refLabel, test.hasRefLabel, "observation.ref_label")
			scannerAssertGitPlumbing(t, fixture.git, fixture.root)
		})
	}
}

func TestUCIScannerUsesGitNULCandidateCensusAndNestedIgnores(t *testing.T) {
	fixture := newScannerFixture(t, false)
	trackedBody := []byte("package tracked\n")
	keptBody := []byte("package kept\n")
	unicodeBody := []byte("package unicode\r\n")
	fixture.write(t, ".gitignore", []byte("generated/\nvendor/\n"))
	fixture.write(t, "src/.gitignore", []byte("*\n!kept.go\n"))
	fixture.write(t, "generated/tracked.go", trackedBody)
	fixture.write(t, "src/kept.go", keptBody)
	fixture.write(t, "src/ignored.go", []byte("package ignored\n"))
	fixture.write(t, "vendor/generated.go", []byte("package vendor\n"))
	fixture.write(t, "space dir/файл name.go", unicodeBody)
	fixture.git.tracked = []scannerGitPath{{Mode: "100644", Path: "generated/tracked.go"}}
	fixture.git.untracked = []string{"src/kept.go", "space dir/файл name.go"}

	result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	scannerAssertCensus(t, result, IndexScanComplete, true, true)
	scannerAssertFile(t, result, "generated/tracked.go", IndexFilePresent, ScannerExclusionNone, trackedBody)
	scannerAssertFile(t, result, "src/kept.go", IndexFilePresent, ScannerExclusionNone, keptBody)
	scannerAssertFile(t, result, "space dir/файл name.go", IndexFilePresent, ScannerExclusionNone, unicodeBody)
	scannerAssertFileAbsent(t, result, "src/ignored.go")
	scannerAssertFileAbsent(t, result, "vendor/generated.go")
	if got := fixture.files.readCount(fixture.path("src/ignored.go")); got != 0 {
		t.Fatalf("ignored file reads = %d, want 0", got)
	}
	if got := fixture.files.readCount(fixture.path("vendor/generated.go")); got != 0 {
		t.Fatalf("ignored vendor file reads = %d, want 0", got)
	}
	scannerAssertGitPlumbing(t, fixture.git, fixture.root)
}

func TestUCIScannerPreservesWindowsUnicodeCaseAndCRLFBytes(t *testing.T) {
	left := newScannerFixture(t, false)
	leftBody := []byte("package fixture\r\n\r\nfunc Left() {}\r\n")
	left.write(t, "Код/Readme.go", leftBody)
	left.git.tracked = []scannerGitPath{{Mode: "100644", Path: "Код/Readme.go"}}

	right := newScannerFixture(t, false)
	rightBody := []byte("package fixture\r\n\r\nfunc Right() {}\r\n")
	right.write(t, "Код/README.go", rightBody)
	right.git.tracked = []scannerGitPath{{Mode: "100644", Path: "Код/README.go"}}

	leftResult, err := scannerScan(left, ScannerPolicy{IncludeUntracked: true})
	if err != nil {
		t.Fatalf("left Scan() error = %v", err)
	}
	rightResult, err := scannerScan(right, ScannerPolicy{IncludeUntracked: true})
	if err != nil {
		t.Fatalf("right Scan() error = %v", err)
	}

	leftFile := scannerFile(t, leftResult, "Код/Readme.go")
	rightFile := scannerFile(t, rightResult, "Код/README.go")
	if !bytes.Equal(leftFile.Body, leftBody) {
		t.Fatalf("left CRLF body = %q, want %q", leftFile.Body, leftBody)
	}
	if !bytes.Equal(rightFile.Body, rightBody) {
		t.Fatalf("right CRLF body = %q, want %q", rightFile.Body, rightBody)
	}
	if leftFile.Path == rightFile.Path {
		t.Fatalf("case-distinct Git paths collapsed to %q", leftFile.Path)
	}
	if !strings.EqualFold(leftFile.Path, rightFile.Path) {
		t.Fatalf("fixture paths are not case variants: %q and %q", leftFile.Path, rightFile.Path)
	}
	scannerAssertGitPlumbing(t, left.git, left.root)
	scannerAssertGitPlumbing(t, right.git, right.root)
}

func TestUCIScannerExcludesNestedRepositoriesAndDeclaredSubmodules(t *testing.T) {
	fixture := newScannerFixture(t, false)
	fixture.write(t, "safe.go", []byte("package safe\n"))
	fixture.write(t, "nested/.git", []byte("gitdir: fixture-nested\n"))
	fixture.write(t, "nested/private.go", []byte("package nested\n"))
	fixture.write(t, ".agent/.git", []byte("gitdir: fixture-agent\n"))
	fixture.write(t, ".agent/transcript.md", []byte("private transcript\n"))
	fixture.mkdir(t, "modules/child")
	fixture.git.tracked = []scannerGitPath{
		{Mode: "100644", Path: "safe.go"},
		{Mode: "160000", Path: "modules/child"},
	}
	fixture.git.untracked = []string{"nested/private.go", ".agent/transcript.md"}

	result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	scannerAssertCensus(t, result, IndexScanComplete, true, true)
	scannerAssertFile(t, result, "safe.go", IndexFilePresent, ScannerExclusionNone, []byte("package safe\n"))
	scannerAssertFile(t, result, "nested/private.go", IndexFileExcluded, ScannerExclusionNestedRepository, nil)
	scannerAssertFile(t, result, ".agent/transcript.md", IndexFileExcluded, ScannerExclusionNestedRepository, nil)
	scannerAssertFile(t, result, "modules/child", IndexFileExcluded, ScannerExclusionSubmodule, nil)
	if got := fixture.files.readCount(fixture.path("nested/private.go")); got != 0 {
		t.Fatalf("nested repository body reads = %d, want 0", got)
	}
	if got := fixture.files.readCount(fixture.path(".agent/transcript.md")); got != 0 {
		t.Fatalf("nested .agent repository body reads = %d, want 0", got)
	}
	if got := fixture.files.readCount(fixture.path("modules/child")); got != 0 {
		t.Fatalf("submodule body reads = %d, want 0", got)
	}
	scannerAssertGitPlumbing(t, fixture.git, fixture.root)
}

func TestUCIScannerRefusesReparseEscapesAndUnsupportedTypes(t *testing.T) {
	fixture := newScannerFixture(t, false)
	fixture.write(t, "safe.go", []byte("package safe\n"))
	fixture.write(t, "escape/symlink.go", []byte("outside body must never be read\n"))
	fixture.write(t, "escape/junction.go", []byte("outside body must never be read\n"))
	fixture.write(t, "unsupported.pipe", []byte("not a regular file\n"))
	fixture.write(t, "binary.dat", []byte{0x00, 0x01, 0x02})
	fixture.files.setMode(fixture.path("escape/symlink.go"), fs.ModeSymlink)
	fixture.files.markReparse(fixture.path("escape/junction.go"))
	fixture.files.setMode(fixture.path("unsupported.pipe"), fs.ModeNamedPipe)
	fixture.git.tracked = []scannerGitPath{
		{Mode: "100644", Path: "safe.go"},
		{Mode: "100644", Path: "escape/symlink.go"},
		{Mode: "100644", Path: "escape/junction.go"},
		{Mode: "100644", Path: "unsupported.pipe"},
		{Mode: "100644", Path: "binary.dat"},
	}

	result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	scannerAssertCensus(t, result, IndexScanComplete, true, true)
	scannerAssertFile(t, result, "safe.go", IndexFilePresent, ScannerExclusionNone, []byte("package safe\n"))
	scannerAssertFile(t, result, "escape/symlink.go", IndexFileExcluded, ScannerExclusionReparseEscape, nil)
	scannerAssertFile(t, result, "escape/junction.go", IndexFileExcluded, ScannerExclusionReparseEscape, nil)
	scannerAssertFile(t, result, "unsupported.pipe", IndexFileExcluded, ScannerExclusionUnsupportedType, nil)
	scannerAssertFile(t, result, "binary.dat", IndexFileExcluded, ScannerExclusionBinary, nil)
	for _, relativePath := range []string{"escape/symlink.go", "escape/junction.go", "unsupported.pipe"} {
		if got := fixture.files.readCount(fixture.path(relativePath)); got != 0 {
			t.Fatalf("unsafe %q body reads = %d, want 0", relativePath, got)
		}
	}
	if got := result.Coverage.ExcludedFiles; got != 4 {
		t.Fatalf("coverage.excluded_files = %d, want 4", got)
	}
	scannerAssertGitPlumbing(t, fixture.git, fixture.root)
}

func TestUCIScannerExcludesOversizedSecretAndProtectedFiles(t *testing.T) {
	fixture := newScannerFixture(t, false)
	fixture.write(t, "source.go", []byte("package source\n"))
	fixture.write(t, "large.go", []byte(strings.Repeat("x", 64)))
	fixture.write(t, "binary.bin", []byte{0x01, 0x00, 0x02})
	fixture.write(t, "credentials.txt", []byte("-----BEGIN PRIVATE KEY-----\nfixture\n"))
	fixture.write(t, ".env", []byte("SAFE_FIXTURE_VALUE=1\n"))
	fixture.write(t, "private/opaque.txt", []byte("fixture-private\n"))
	fixture.git.tracked = []scannerGitPath{
		{Mode: "100644", Path: "source.go"},
		{Mode: "100644", Path: "large.go"},
		{Mode: "100644", Path: "binary.bin"},
		{Mode: "100644", Path: "credentials.txt"},
		{Mode: "100644", Path: ".env"},
		{Mode: "100644", Path: "private/opaque.txt"},
	}

	policy := ScannerPolicy{
		IncludeUntracked: true,
		MaxFileBytes:     32,
		ProtectedPaths:   []string{".env", "private/opaque.txt"},
		SecretDetector:   scannerFixtureSecretDetector{marker: []byte("BEGIN PRIVATE KEY")},
	}
	result, err := scannerScan(fixture, policy)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	scannerAssertCensus(t, result, IndexScanComplete, true, true)
	scannerAssertFile(t, result, "source.go", IndexFilePresent, ScannerExclusionNone, []byte("package source\n"))
	scannerAssertFile(t, result, "large.go", IndexFileExcluded, ScannerExclusionTooLarge, nil)
	scannerAssertFile(t, result, "binary.bin", IndexFileExcluded, ScannerExclusionBinary, nil)
	scannerAssertFile(t, result, "credentials.txt", IndexFileExcluded, ScannerExclusionSecret, nil)
	scannerAssertFile(t, result, ".env", IndexFileExcluded, ScannerExclusionProtected, nil)
	scannerAssertFile(t, result, "private/opaque.txt", IndexFileExcluded, ScannerExclusionProtected, nil)
	if got := result.Coverage.ExcludedFiles; got != 5 {
		t.Fatalf("coverage.excluded_files = %d, want 5", got)
	}
	scannerAssertGitPlumbing(t, fixture.git, fixture.root)
}

func TestUCIScannerRecordsPhaseDiagnostics(t *testing.T) {
	fixture := newScannerFixture(t, false)
	source := []byte("package source\n")
	binary := []byte{0x01, 0x00, 0x02}
	secret := []byte("fixture-secret-marker\n")
	fixture.write(t, "source.go", source)
	fixture.write(t, "binary.bin", binary)
	fixture.write(t, "secret.txt", secret)
	fixture.write(t, "private.txt", []byte("protected\n"))
	fixture.write(t, "unreadable.txt", []byte("unreadable\n"))
	fixture.git.tracked = []scannerGitPath{
		{Mode: "100644", Path: "source.go"},
		{Mode: "100644", Path: "binary.bin"},
		{Mode: "100644", Path: "secret.txt"},
		{Mode: "100644", Path: "private.txt"},
		{Mode: "100644", Path: "unreadable.txt"},
	}
	for _, operation := range []string{
		"rev-parse --show-toplevel --absolute-git-dir --git-common-dir --git-path HEAD --show-object-format",
		"status --porcelain=v2 --branch -z --untracked-files=all",
		"ls-files --stage --others --exclude-standard -t -z",
	} {
		fixture.git.delays[operation] = time.Millisecond
	}
	fixture.files.denyRead[fixture.path("unreadable.txt")] = fs.ErrPermission

	result, err := scannerScan(fixture, ScannerPolicy{
		IncludeUntracked: true,
		ProtectedPaths:   []string{"private.txt"},
		SecretDetector:   scannerFixtureSecretDetector{marker: []byte("fixture-secret-marker")},
	})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	scannerAssertCensus(t, result, IndexScanIncomplete, false, false)
	scannerAssertGitPlumbing(t, fixture.git, fixture.root)

	diagnostics := result.Diagnostics
	for name, duration := range map[string]time.Duration{
		"topology":   diagnostics.GitTopologyDuration,
		"status":     diagnostics.GitStatusDuration,
		"candidates": diagnostics.GitCandidatesDuration,
	} {
		if duration < time.Millisecond {
			t.Fatalf("%s Git duration = %s, want at least %s", name, duration, time.Millisecond)
		}
	}
	if diagnostics.GitStagedDuration != 0 || diagnostics.GitUntrackedDuration != 0 {
		t.Fatalf("separate candidate durations = staged %s, untracked %s, want zero for one combined invocation", diagnostics.GitStagedDuration, diagnostics.GitUntrackedDuration)
	}
	if diagnostics.CandidateCount != 5 || diagnostics.AdmittedCount != 1 || diagnostics.ExcludedCount != 3 || diagnostics.UnreadableCount != 1 {
		t.Fatalf("diagnostic counters = %#v, want candidates=5 admitted=1 excluded=3 unreadable=1", diagnostics)
	}
	if want := int64(len(source) + len(binary) + len(secret)); diagnostics.BytesRead != want {
		t.Fatalf("diagnostic bytes read = %d, want %d", diagnostics.BytesRead, want)
	}
	components := diagnostics.GitTopologyDuration + diagnostics.GitStatusDuration + diagnostics.GitCandidatesDuration + diagnostics.GitStagedDuration + diagnostics.GitUntrackedDuration + diagnostics.CandidateLoopDuration + diagnostics.ResidualDuration
	if diagnostics.TotalDuration != components {
		t.Fatalf("diagnostic durations do not reconcile: total=%s components=%s diagnostics=%#v", diagnostics.TotalDuration, components, diagnostics)
	}
}

func TestUCIScannerFailsClosedForUnavailableAndChangingInputs(t *testing.T) {
	t.Run("inaccessible authorized root", func(t *testing.T) {
		fixture := newScannerFixture(t, false)
		fixture.files.denyLstat[fixture.root] = fs.ErrPermission

		result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
		if err == nil {
			t.Fatal("Scan() error = nil, want inaccessible-root error")
		}
		scannerAssertCensus(t, result, IndexScanFailed, false, false)
		if got := len(fixture.git.calls); got != 0 {
			t.Fatalf("Git calls after inaccessible root = %d, want 0", got)
		}
	})

	t.Run("inaccessible descendant is unreadable not deleted", func(t *testing.T) {
		fixture := newScannerFixture(t, false)
		fixture.write(t, "blocked/tree.go", []byte("package blocked\n"))
		fixture.git.tracked = []scannerGitPath{{Mode: "100644", Path: "blocked/tree.go"}}
		fixture.files.denyRead[fixture.path("blocked/tree.go")] = fs.ErrPermission

		result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
		if err != nil {
			t.Fatalf("Scan() error = %v, want incomplete result", err)
		}
		scannerAssertCensus(t, result, IndexScanIncomplete, false, false)
		scannerAssertFile(t, result, "blocked/tree.go", IndexFileUnreadable, ScannerExclusionNone, nil)
		if got := result.Coverage.UnreadableFiles; got != 1 {
			t.Fatalf("coverage.unreadable_files = %d, want 1", got)
		}
		if got := result.Coverage.Structural; got != IndexCoveragePartial {
			t.Fatalf("coverage.structural = %q, want %q", got, IndexCoveragePartial)
		}
	})

	t.Run("file changing across stat read stat is unreadable", func(t *testing.T) {
		fixture := newScannerFixture(t, false)
		fixture.write(t, "changing.go", []byte("package before\n"))
		fixture.git.tracked = []scannerGitPath{{Mode: "100644", Path: "changing.go"}}
		fixture.files.markChanging(fixture.path("changing.go"))

		result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
		if err != nil {
			t.Fatalf("Scan() error = %v, want incomplete result", err)
		}
		scannerAssertCensus(t, result, IndexScanIncomplete, false, false)
		scannerAssertFile(t, result, "changing.go", IndexFileUnreadable, ScannerExclusionChanging, nil)
		if got := result.Coverage.UnreadableFiles; got != 1 {
			t.Fatalf("coverage.unreadable_files = %d, want 1", got)
		}
	})
}

func TestUCIScannerRejectsMismatchedEvidenceAndNonRootRelativeCandidates(t *testing.T) {
	t.Run("Git top level must match authorized root evidence", func(t *testing.T) {
		fixture := newScannerFixture(t, false)
		fixture.git.showTopLevel = filepath.Join(t.TempDir(), "different-root")

		result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
		if err == nil {
			t.Fatal("Scan() error = nil, want root-evidence mismatch")
		}
		scannerAssertCensus(t, result, IndexScanFailed, false, false)
		if got := len(fixture.files.readCalls); got != 0 {
			t.Fatalf("body reads after root-evidence mismatch = %d, want 0", got)
		}
	})

	t.Run("Git candidate cannot escape root", func(t *testing.T) {
		fixture := newScannerFixture(t, false)
		outside := filepath.Join(t.TempDir(), "outside.go")
		if err := os.WriteFile(outside, []byte("outside body must never be read\n"), 0o600); err != nil {
			t.Fatalf("write outside fixture: %v", err)
		}
		fixture.git.tracked = []scannerGitPath{{Mode: "100644", Path: "../outside.go"}}

		result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
		if err == nil {
			t.Fatal("Scan() error = nil, want non-root-relative Git candidate rejection")
		}
		scannerAssertCensus(t, result, IndexScanFailed, false, false)
		if got := fixture.files.readCount(outside); got != 0 {
			t.Fatalf("outside body reads = %d, want 0", got)
		}
	})
}

func TestUCIScannerCensusCannotAuthorizeDeleteAllByAccident(t *testing.T) {
	t.Run("complete empty census is explicit", func(t *testing.T) {
		fixture := newScannerFixture(t, false)

		result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
		if err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		scannerAssertCensus(t, result, IndexScanComplete, true, true)
		if got := len(result.Files); got != 0 {
			t.Fatalf("complete empty scan files = %d, want 0", got)
		}
	})

	t.Run("empty Git enumeration failure is not delete all", func(t *testing.T) {
		fixture := newScannerFixture(t, false)
		fixture.git.failures["ls-files --stage --others --exclude-standard -t -z"] = errors.New("fixture candidate enumeration unavailable")

		result, err := scannerScan(fixture, ScannerPolicy{IncludeUntracked: true})
		if err == nil {
			t.Fatal("Scan() error = nil, want enumeration failure")
		}
		scannerAssertCensus(t, result, IndexScanFailed, false, false)
		if got := len(result.Files); got != 0 {
			t.Fatalf("failed empty scan files = %d, want 0", got)
		}
	})
}

func TestUCIScannerCombinedCandidatesMatchSeparateGitOutput(t *testing.T) {
	root := t.TempDir()
	if err := exec.CommandContext(t.Context(), "git", "init", root).Run(); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.go"), []byte("package tracked\n"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	if err := exec.CommandContext(t.Context(), "git", "-C", root, "add", "--", "tracked.go").Run(); err != nil {
		t.Fatalf("stage tracked file: %v", err)
	}
	untrackedPath := "untracked.go"
	if err := os.WriteFile(filepath.Join(root, untrackedPath), []byte("package untracked\n"), 0o600); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	runner := ExecGitRunner{}
	run := func(command ...string) []byte {
		t.Helper()
		result, err := runner.Run(t.Context(), GitInvocation{Args: scannerGitArgs(root, command...), Env: scannerGitEnvironment()})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git %q failed: result=%#v err=%v", command, result, err)
		}
		return result.Stdout
	}

	separateStaged, err := scannerStagedCandidates(run("ls-files", "--stage", "-z"), "sha1")
	if err != nil {
		t.Fatalf("parse separate staged output: %v", err)
	}
	separateUntracked, err := scannerUntrackedCandidates(run("ls-files", "--others", "--exclude-standard", "-z"))
	if err != nil {
		t.Fatalf("parse separate untracked output: %v", err)
	}
	combinedStaged, combinedUntracked, err := scannerCombinedCandidates(run("ls-files", "--stage", "--others", "--exclude-standard", "-t", "-z"), "sha1")
	if err != nil {
		t.Fatalf("parse combined candidate output: %v", err)
	}
	if !reflect.DeepEqual(combinedStaged, separateStaged) || !reflect.DeepEqual(combinedUntracked, separateUntracked) {
		t.Fatalf("combined candidate output differs from separate output: staged=%#v want=%#v untracked=%#v want=%#v", combinedStaged, separateStaged, combinedUntracked, separateUntracked)
	}

	want, err := scannerMergeCandidates(separateStaged, separateUntracked, true)
	if err != nil {
		t.Fatalf("merge separate candidates: %v", err)
	}
	got, err := scannerMergeCandidates(combinedStaged, combinedUntracked, true)
	if err != nil {
		t.Fatalf("merge combined candidates: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("combined merged candidates = %#v, want %#v", got, want)
	}
}

func TestUCIScannerCombinedCandidatesKeepTaggedUntrackedDistinct(t *testing.T) {
	path := "100644 " + strings.Repeat("a", 40) + " 0\tuntracked.go"
	staged, untracked, err := scannerCombinedCandidates([]byte("? "+path+"\x00"), "sha1")
	if err != nil {
		t.Fatalf("parse tagged untracked candidate: %v", err)
	}
	if len(staged) != 0 || !reflect.DeepEqual(untracked, []scannerCandidate{{path: path}}) {
		t.Fatalf("tagged untracked candidate = staged %#v untracked %#v, want path %q", staged, untracked, path)
	}
}

func TestUCIScannerCachesTopologyAndKeepsStatusLive(t *testing.T) {
	fixture := newScannerFixture(t, false)
	fixture.write(t, "clean.go", []byte("package clean\n"))
	fixture.write(t, "staged.go", []byte("package staged\n"))
	fixture.write(t, "untracked.go", []byte("package untracked\n"))
	fixture.git.tracked = []scannerGitPath{{Mode: "100644", Path: "clean.go"}, {Mode: "100644", Path: "staged.go"}}
	fixture.git.untracked = []string{"untracked.go"}

	scanner := NewScanner(fixture.git, fixture.files, ScannerPolicy{IncludeUntracked: true})
	evidence := AuthorizedRootEvidence{RootPath: fixture.root}
	result, err := scanner.Scan(t.Context(), evidence)
	if err != nil {
		t.Fatalf("initial Scan() error = %v", err)
	}
	scannerAssertGitPlumbing(t, fixture.git, fixture.root)
	scannerAssertFile(t, result, "clean.go", IndexFilePresent, ScannerExclusionNone, []byte("package clean\n"))
	scannerAssertFile(t, result, "staged.go", IndexFilePresent, ScannerExclusionNone, []byte("package staged\n"))
	scannerAssertFile(t, result, "untracked.go", IndexFilePresent, ScannerExclusionNone, []byte("package untracked\n"))

	fixture.git.hasRefLabel = false
	result, err = scanner.Scan(t.Context(), evidence)
	if err != nil {
		t.Fatalf("detached Scan() error = %v", err)
	}
	scannerAssertOptionalString(t, result.Observation.HeadOID, fixture.git.headOID, true, "detached observation.head_oid")
	scannerAssertOptionalString(t, result.Observation.RefLabel, "", false, "detached observation.ref_label")
	scannerAssertGitOperations(t, fixture.git, fixture.root, 3,
		"status --porcelain=v2 --branch -z --untracked-files=all",
	)

	fixture.git.hasHeadOID = false
	fixture.git.refLabel = "main"
	fixture.git.hasRefLabel = true
	result, err = scanner.Scan(t.Context(), evidence)
	if err != nil {
		t.Fatalf("unborn Scan() error = %v", err)
	}
	scannerAssertOptionalString(t, result.Observation.HeadOID, "", false, "unborn observation.head_oid")
	scannerAssertOptionalString(t, result.Observation.RefLabel, "main", true, "unborn observation.ref_label")
	scannerAssertGitOperations(t, fixture.git, fixture.root, 4,
		"status --porcelain=v2 --branch -z --untracked-files=all",
	)
}

func TestUCIScannerInvalidatesCachedTopologyForMarkerChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		linked bool
	}{
		{name: "ordinary marker"},
		{name: "linked worktree marker", linked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScannerFixture(t, test.linked)
			scanner := NewScanner(fixture.git, fixture.files, ScannerPolicy{IncludeUntracked: true})
			evidence := AuthorizedRootEvidence{RootPath: fixture.root}
			for scan := range 2 {
				if _, err := scanner.Scan(t.Context(), evidence); err != nil {
					t.Fatalf("Scan(%d) error = %v", scan, err)
				}
			}
			scannerAssertGitCallCount(t, fixture.git, 4)

			marker := fixture.path(".git")
			if test.linked {
				if err := os.WriteFile(marker, []byte("gitdir: "+fixture.git.gitDir+"-replacement\n"), 0o600); err != nil {
					t.Fatalf("replace linked marker content: %v", err)
				}
			} else {
				if err := os.RemoveAll(marker); err != nil {
					t.Fatalf("remove ordinary marker: %v", err)
				}
				if err := os.WriteFile(marker, []byte("gitdir: "+fixture.git.gitDir+"\n"), 0o600); err != nil {
					t.Fatalf("replace ordinary marker: %v", err)
				}
			}
			if _, err := scanner.Scan(t.Context(), evidence); err != nil {
				t.Fatalf("Scan after marker replacement error = %v", err)
			}
			scannerAssertGitCallCount(t, fixture.git, 7)

			if err := os.RemoveAll(marker); err != nil {
				t.Fatalf("remove marker: %v", err)
			}
			if _, err := scanner.Scan(t.Context(), evidence); !errors.Is(err, ErrScannerInvalidRoot) {
				t.Fatalf("Scan after marker removal error = %v, want ErrScannerInvalidRoot", err)
			}
			scannerAssertGitCallCount(t, fixture.git, 7)

			if test.linked {
				if err := os.WriteFile(marker, []byte("gitdir: "+fixture.git.gitDir+"\n"), 0o600); err != nil {
					t.Fatalf("restore linked marker: %v", err)
				}
			} else {
				if err := os.MkdirAll(marker, 0o755); err != nil {
					t.Fatalf("restore ordinary marker: %v", err)
				}
			}
			if _, err := scanner.Scan(t.Context(), evidence); err != nil {
				t.Fatalf("Scan after marker restoration error = %v", err)
			}
			scannerAssertGitCallCount(t, fixture.git, 10)
		})
	}
}

func TestUCIScannerRecoversAfterCachedTopologyFailure(t *testing.T) {
	fixture := newScannerFixture(t, true)
	scanner := NewScanner(fixture.git, fixture.files, ScannerPolicy{IncludeUntracked: true})
	evidence := AuthorizedRootEvidence{RootPath: fixture.root}
	if _, err := scanner.Scan(t.Context(), evidence); err != nil {
		t.Fatalf("initial Scan() error = %v", err)
	}

	marker := fixture.path(".git")
	if err := os.WriteFile(marker, []byte("gitdir: "+fixture.git.gitDir+"-changed\n"), 0o600); err != nil {
		t.Fatalf("change marker content: %v", err)
	}
	topologyOperation := "rev-parse --show-toplevel --absolute-git-dir --git-common-dir --git-path HEAD --show-object-format"
	fixture.git.failures[topologyOperation] = errors.New("fixture topology unavailable")
	if _, err := scanner.Scan(t.Context(), evidence); err == nil {
		t.Fatal("Scan() error = nil after topology failure")
	}
	scannerAssertGitCallCount(t, fixture.git, 4)

	delete(fixture.git.failures, topologyOperation)
	if _, err := scanner.Scan(t.Context(), evidence); err != nil {
		t.Fatalf("Scan after topology recovery error = %v", err)
	}
	scannerAssertGitCallCount(t, fixture.git, 7)
}

func TestUCIScannerCandidateCacheMatchesUncachedRealGit(t *testing.T) {
	t.Run("modified tracked file", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		scannerRealWrite(t, fixture.root, "tracked.go", []byte("package fixture\nconst Version = 2\n"))
		_, calls := fixture.scanAndCompare(t)
		scannerAssertRecordedGitOperations(t, calls, fixture.root, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("new untracked file", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		scannerRealWrite(t, fixture.root, "new.go", []byte("package fixture\n"))
		result, calls := fixture.scanAndCompare(t)
		scannerAssertFile(t, result, "new.go", IndexFilePresent, ScannerExclusionNone, []byte("package fixture\n"))
		scannerAssertRecordedGitOperations(t, calls, fixture.root, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("nested ignore negation", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		scannerRealWrite(t, fixture.root, "src/.gitignore", []byte("*\n!kept.go\n"))
		scannerRealWrite(t, fixture.root, "src/kept.go", []byte("package kept\n"))
		scannerRealWrite(t, fixture.root, "src/ignored.go", []byte("package ignored\n"))
		result, calls := fixture.scanAndCompare(t)
		scannerAssertFile(t, result, "src/kept.go", IndexFilePresent, ScannerExclusionNone, []byte("package kept\n"))
		scannerAssertFileAbsent(t, result, "src/ignored.go")
		scannerAssertRecordedGitOperations(t, calls, fixture.root, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("deleted tracked file", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		if err := os.Remove(filepath.Join(fixture.root, "tracked.go")); err != nil {
			t.Fatalf("remove tracked file: %v", err)
		}
		result, calls := fixture.scanAndCompare(t)
		scannerAssertFileAbsent(t, result, "tracked.go")
		scannerAssertRecordedGitOperations(t, calls, fixture.root, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("unstaged rename", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		if err := os.Rename(filepath.Join(fixture.root, "tracked.go"), filepath.Join(fixture.root, "renamed.go")); err != nil {
			t.Fatalf("rename tracked file: %v", err)
		}
		result, calls := fixture.scanAndCompare(t)
		scannerAssertFile(t, result, "renamed.go", IndexFilePresent, ScannerExclusionNone, []byte("package fixture\nconst Version = 1\n"))
		scannerAssertRecordedGitOperations(t, calls, fixture.root, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("staged file and rename", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		scannerRealWrite(t, fixture.root, "tracked.go", []byte("package fixture\nconst Version = 3\n"))
		scannerRealGit(t, fixture.root, "add", "--", "tracked.go")
		_, calls := fixture.scanAndCompare(t)
		scannerAssertRecordedGitOperations(t, calls, fixture.root,
			"status --porcelain=v2 --branch -z --untracked-files=all",
			"ls-files --stage --others --exclude-standard -t -z",
		)

		scannerRealGit(t, fixture.root, "mv", "-f", "tracked.go", "renamed.go")
		result, calls := fixture.scanAndCompare(t)
		scannerAssertFile(t, result, "renamed.go", IndexFilePresent, ScannerExclusionNone, []byte("package fixture\nconst Version = 3\n"))
		scannerAssertRecordedGitOperations(t, calls, fixture.root,
			"status --porcelain=v2 --branch -z --untracked-files=all",
			"ls-files --stage --others --exclude-standard -t -z",
		)
	})

	t.Run("case-only staged rename", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		scannerRealGit(t, fixture.root, "mv", "-f", "Case.go", "case.go")
		result, calls := fixture.scanAndCompare(t)
		scannerAssertFile(t, result, "case.go", IndexFilePresent, ScannerExclusionNone, []byte("package fixture\nconst Case = true\n"))
		scannerAssertFileAbsent(t, result, "Case.go")
		scannerAssertRecordedGitOperations(t, calls, fixture.root,
			"status --porcelain=v2 --branch -z --untracked-files=all",
			"ls-files --stage --others --exclude-standard -t -z",
		)
	})

	t.Run("branch switch and detached head", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		scannerRealGit(t, fixture.root, "branch", "cache-branch")
		scannerRealGit(t, fixture.root, "switch", "cache-branch")
		result, calls := fixture.scanAndCompare(t)
		scannerAssertOptionalString(t, result.Observation.RefLabel, "cache-branch", true, "branch observation.ref_label")
		scannerAssertRecordedGitOperations(t, calls, fixture.root, "status --porcelain=v2 --branch -z --untracked-files=all")

		scannerRealGit(t, fixture.root, "checkout", "--detach")
		result, calls = fixture.scanAndCompare(t)
		scannerAssertOptionalString(t, result.Observation.RefLabel, "", false, "detached observation.ref_label")
		scannerAssertRecordedGitOperations(t, calls, fixture.root, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("unborn repository", func(t *testing.T) {
		root := t.TempDir()
		scannerRealGit(t, root, "init", "-b", "main")
		scannerRealWrite(t, root, "unborn.go", []byte("package unborn\n"))
		fixture := newScannerRealFixtureForRoot(root)
		fixture.warm(t)
		result, calls := fixture.scanAndCompare(t)
		scannerAssertFile(t, result, "unborn.go", IndexFilePresent, ScannerExclusionNone, []byte("package unborn\n"))
		scannerAssertRecordedGitOperations(t, calls, root, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("linked worktree", func(t *testing.T) {
		primary := newScannerRealFixture(t)
		linkedRoot := filepath.Join(t.TempDir(), "linked")
		scannerRealGit(t, primary.root, "worktree", "add", "--detach", linkedRoot, "HEAD")
		fixture := newScannerRealFixtureForRoot(linkedRoot)
		fixture.warm(t)
		_, calls := fixture.scanAndCompare(t)
		scannerAssertRecordedGitOperations(t, calls, linkedRoot, "status --porcelain=v2 --branch -z --untracked-files=all")
	})

	t.Run("index content changes with identical size and modification time", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.warm(t)
		indexPath := scannerRealGitPath(t, fixture.root, "index")
		scannerRealWrite(t, fixture.root, "tracked.go", []byte("package fixture\nconst Version = 2\n"))
		scannerRealGit(t, fixture.root, "add", "--", "tracked.go")
		_, refreshCalls := fixture.scanAndCompare(t)
		scannerAssertRecordedGitOperations(t, refreshCalls, fixture.root,
			"status --porcelain=v2 --branch -z --untracked-files=all",
			"ls-files --stage --others --exclude-standard -t -z",
		)
		beforeInfo, err := (OSScannerFileSystem{}).Lstat(indexPath)
		if err != nil {
			t.Fatalf("stat index before replacement: %v", err)
		}
		before, err := os.ReadFile(indexPath)
		if err != nil {
			t.Fatalf("read index before replacement: %v", err)
		}
		scannerRealWrite(t, fixture.root, "tracked.go", []byte("package fixture\nconst Version = 3\n"))
		scannerRealGit(t, fixture.root, "add", "--", "tracked.go")
		after, err := os.ReadFile(indexPath)
		if err != nil {
			t.Fatalf("read index after replacement: %v", err)
		}
		if len(before) != len(after) || bytes.Equal(before, after) {
			t.Fatalf("index replacement did not create an equal-size content change")
		}
		if err := os.Chtimes(indexPath, time.Now(), beforeInfo.ModTime); err != nil {
			t.Fatalf("restore index modification time: %v", err)
		}
		afterInfo, err := (OSScannerFileSystem{}).Lstat(indexPath)
		if err != nil {
			t.Fatalf("stat index after timestamp restoration: %v", err)
		}
		if !scannerSameFileInfo(beforeInfo, afterInfo) {
			t.Fatalf("index replacement fingerprint changed despite equal size and modification time")
		}
		_, calls := fixture.scanAndCompare(t)
		scannerAssertRecordedGitOperations(t, calls, fixture.root,
			"status --porcelain=v2 --branch -z --untracked-files=all",
			"ls-files --stage --others --exclude-standard -t -z",
		)
	})

	t.Run("failed candidate enumeration retries", func(t *testing.T) {
		fixture := newScannerRealFixture(t)
		fixture.runner.failOperation = "ls-files --stage --others --exclude-standard -t -z"
		fixture.runner.failuresRemaining = 1
		if _, err := fixture.scanner.Scan(t.Context(), AuthorizedRootEvidence{RootPath: fixture.root}); err == nil {
			t.Fatal("Scan() error = nil after candidate enumeration failure")
		}
		scannerAssertRecordedGitOperations(t, fixture.runner.calls, fixture.root,
			"rev-parse --show-toplevel --absolute-git-dir --git-common-dir --git-path HEAD --show-object-format",
			"status --porcelain=v2 --branch -z --untracked-files=all",
			"ls-files --stage --others --exclude-standard -t -z",
		)
		_, calls := fixture.scanAndCompare(t)
		scannerAssertRecordedGitOperations(t, calls, fixture.root,
			"status --porcelain=v2 --branch -z --untracked-files=all",
			"ls-files --stage --others --exclude-standard -t -z",
		)
	})
}

type scannerRealFixture struct {
	root    string
	runner  *scannerRecordingGitRunner
	scanner *Scanner
}

func newScannerRealFixture(t *testing.T) *scannerRealFixture {
	t.Helper()
	root := t.TempDir()
	scannerRealGit(t, root, "init", "-b", "main")
	scannerRealGit(t, root, "config", "user.email", "scanner@example.test")
	scannerRealGit(t, root, "config", "user.name", "Scanner Fixture")
	scannerRealWrite(t, root, "tracked.go", []byte("package fixture\nconst Version = 1\n"))
	scannerRealWrite(t, root, "Case.go", []byte("package fixture\nconst Case = true\n"))
	scannerRealGit(t, root, "add", "--", "tracked.go", "Case.go")
	scannerRealGit(t, root, "commit", "-m", "scanner fixture")
	return newScannerRealFixtureForRoot(root)
}

func newScannerRealFixtureForRoot(root string) *scannerRealFixture {
	runner := &scannerRecordingGitRunner{}
	return &scannerRealFixture{
		root:    root,
		runner:  runner,
		scanner: NewScanner(runner, OSScannerFileSystem{}, ScannerPolicy{IncludeUntracked: true}),
	}
}

func (fixture *scannerRealFixture) warm(t *testing.T) {
	t.Helper()
	_, calls := fixture.scanAndCompare(t)
	scannerAssertRecordedGitOperations(t, calls, fixture.root,
		"rev-parse --show-toplevel --absolute-git-dir --git-common-dir --git-path HEAD --show-object-format",
		"status --porcelain=v2 --branch -z --untracked-files=all",
		"ls-files --stage --others --exclude-standard -t -z",
	)
}

func (fixture *scannerRealFixture) scanAndCompare(t *testing.T) (ScannerResult, []GitInvocation) {
	t.Helper()
	start := len(fixture.runner.calls)
	result, err := fixture.scanner.Scan(t.Context(), AuthorizedRootEvidence{RootPath: fixture.root})
	if err != nil {
		t.Fatalf("cached Scan() error = %v", err)
	}
	uncached, err := NewScanner(ExecGitRunner{}, OSScannerFileSystem{}, ScannerPolicy{IncludeUntracked: true}).Scan(t.Context(), AuthorizedRootEvidence{RootPath: fixture.root})
	if err != nil {
		t.Fatalf("uncached Scan() error = %v", err)
	}
	scannerAssertSemanticallyEqual(t, result, uncached)
	return result, append([]GitInvocation(nil), fixture.runner.calls[start:]...)
}

func scannerAssertSemanticallyEqual(t *testing.T, cached, uncached ScannerResult) {
	t.Helper()
	if !reflect.DeepEqual(cached.Files, uncached.Files) || !reflect.DeepEqual(cached.Census, uncached.Census) || !reflect.DeepEqual(cached.Coverage, uncached.Coverage) || cached.Observation.Dirty != uncached.Observation.Dirty || !reflect.DeepEqual(cached.Observation.HeadOID, uncached.Observation.HeadOID) || !reflect.DeepEqual(cached.Observation.ObjectFormat, uncached.Observation.ObjectFormat) || !reflect.DeepEqual(cached.Observation.RefLabel, uncached.Observation.RefLabel) {
		t.Fatalf("cached scan differs from uncached scan: cached=%#v uncached=%#v", cached, uncached)
	}
}

type scannerRecordingGitRunner struct {
	calls             []GitInvocation
	failOperation     string
	failuresRemaining int
}

func (runner *scannerRecordingGitRunner) Run(ctx context.Context, invocation GitInvocation) (GitResult, error) {
	runner.calls = append(runner.calls, GitInvocation{Args: append([]string(nil), invocation.Args...), Env: append([]string(nil), invocation.Env...)})
	if scannerGitOperation(invocation.Args) == runner.failOperation && runner.failuresRemaining > 0 {
		runner.failuresRemaining--
		return GitResult{}, errors.New("fixture candidate enumeration unavailable")
	}
	return (ExecGitRunner{}).Run(ctx, invocation)
}

func scannerRealGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", arguments, err, output)
	}
	return string(output)
}

func scannerRealGitPath(t *testing.T, root, name string) string {
	t.Helper()
	value := filepath.FromSlash(strings.TrimSpace(scannerRealGit(t, root, "rev-parse", "--git-path", name)))
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	return filepath.Clean(value)
}

func scannerRealWrite(t *testing.T, root, relativePath string, body []byte) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("create parent for %q: %v", relativePath, err)
	}
	if err := os.WriteFile(fullPath, body, 0o600); err != nil {
		t.Fatalf("write %q: %v", relativePath, err)
	}
}

func scannerAssertRecordedGitOperations(t *testing.T, calls []GitInvocation, root string, operations ...string) {
	t.Helper()
	if len(calls) != len(operations) {
		t.Fatalf("Git process count = %d, want %d; calls=%#v", len(calls), len(operations), calls)
	}
	for index, want := range operations {
		call := calls[index]
		if len(call.Args) < 3 || call.Args[0] != "-C" || call.Args[1] != root {
			t.Fatalf("Git invocation is not an argument-vector root-bound call: %#v", call.Args)
		}
		if !scannerHasEnvironment(call.Env, "GIT_OPTIONAL_LOCKS", "0") || !scannerHasEnvironment(call.Env, "GIT_TERMINAL_PROMPT", "0") {
			t.Fatalf("Git invocation omitted required read-only environment: %#v", call.Env)
		}
		if got := scannerGitOperation(call.Args); got != want {
			t.Fatalf("Git operation = %q, want %q", got, want)
		}
	}
}

type scannerFixture struct {
	root  string
	git   *scannerFixtureGit
	files *scannerFixtureFiles
}

func newScannerFixture(t *testing.T, linked bool) *scannerFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "scanner root with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create scanner fixture root: %v", err)
	}

	var gitDir string
	var commonGitDir string
	if linked {
		gitDir = filepath.Join(t.TempDir(), "linked private git dir")
		commonGitDir = filepath.Join(t.TempDir(), "common git dir")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatalf("create linked private git dir: %v", err)
		}
		if err := os.MkdirAll(commonGitDir, 0o755); err != nil {
			t.Fatalf("create linked common git dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
			t.Fatalf("write linked .git file: %v", err)
		}
	} else {
		gitDir = filepath.Join(root, ".git")
		commonGitDir = gitDir
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatalf("create ordinary .git directory: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatalf("write fixture HEAD: %v", err)
	}

	git := &scannerFixtureGit{
		root:         root,
		showTopLevel: root,
		gitDir:       gitDir,
		commonGitDir: commonGitDir,
		headPath:     filepath.Join(gitDir, "HEAD"),
		objectFormat: "sha1",
		headOID:      strings.Repeat("1", 40),
		hasHeadOID:   true,
		refLabel:     "main",
		hasRefLabel:  true,
		failures:     make(map[string]error),
		delays:       make(map[string]time.Duration),
	}
	files := &scannerFixtureFiles{
		denyLstat:    make(map[string]error),
		denyRead:     make(map[string]error),
		modeOverride: make(map[string]fs.FileMode),
		reparse:      make(map[string]bool),
		readCalls:    make(map[string]int),
		changing:     make(map[string]bool),
	}
	return &scannerFixture{root: root, git: git, files: files}
}

func (fixture *scannerFixture) write(t *testing.T, relativePath string, body []byte) {
	t.Helper()
	path := fixture.path(relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture parent for %q: %v", relativePath, err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write fixture %q: %v", relativePath, err)
	}
}

func (fixture *scannerFixture) mkdir(t *testing.T, relativePath string) {
	t.Helper()
	if err := os.MkdirAll(fixture.path(relativePath), 0o755); err != nil {
		t.Fatalf("create fixture directory %q: %v", relativePath, err)
	}
}

func (fixture *scannerFixture) path(relativePath string) string {
	return filepath.Clean(filepath.Join(fixture.root, filepath.FromSlash(relativePath)))
}

func scannerScan(fixture *scannerFixture, policy ScannerPolicy) (ScannerResult, error) {
	scanner := NewScanner(fixture.git, fixture.files, policy)
	return scanner.Scan(context.Background(), AuthorizedRootEvidence{RootPath: fixture.root})
}

type scannerGitPath struct {
	Mode string
	Path string
}

type scannerFixtureGit struct {
	root         string
	showTopLevel string
	gitDir       string
	commonGitDir string
	headPath     string
	objectFormat string
	headOID      string
	hasHeadOID   bool
	refLabel     string
	hasRefLabel  bool
	dirty        bool
	tracked      []scannerGitPath
	untracked    []string
	failures     map[string]error
	delays       map[string]time.Duration
	calls        []GitInvocation
}

func (git *scannerFixtureGit) Run(ctx context.Context, invocation GitInvocation) (GitResult, error) {
	if err := ctx.Err(); err != nil {
		return GitResult{}, err
	}
	copy := GitInvocation{
		Args: append([]string(nil), invocation.Args...),
		Env:  append([]string(nil), invocation.Env...),
	}
	git.calls = append(git.calls, copy)
	operation := scannerGitOperation(invocation.Args)
	if err := git.failures[operation]; err != nil {
		return GitResult{}, err
	}
	if delay := git.delays[operation]; delay > 0 {
		time.Sleep(delay)
	}

	switch operation {
	case "rev-parse --show-toplevel --absolute-git-dir --git-common-dir --git-path HEAD --show-object-format":
		return GitResult{Stdout: []byte(strings.Join([]string{git.showTopLevel, git.gitDir, git.commonGitDir, git.headPath, git.objectFormat}, "\n") + "\n")}, nil
	case "status --porcelain=v2 --branch -z --untracked-files=all":
		return GitResult{Stdout: git.statusOutput()}, nil
	case "ls-files --stage --others --exclude-standard -t -z":
		return GitResult{Stdout: git.combinedOutput()}, nil
	default:
		return GitResult{}, errors.New("unexpected Git argument vector: " + operation)
	}
}

func (git *scannerFixtureGit) stageOutput() []byte {
	objectIDLength := 40
	if git.objectFormat == "sha256" {
		objectIDLength = 64
	}
	objectID := strings.Repeat("a", objectIDLength)
	var output strings.Builder
	for _, entry := range git.tracked {
		output.WriteString(entry.Mode)
		output.WriteByte(' ')
		output.WriteString(objectID)
		output.WriteString(" 0\t")
		output.WriteString(entry.Path)
		output.WriteByte(0)
	}
	return []byte(output.String())
}

func (git *scannerFixtureGit) untrackedOutput() []byte {
	var output strings.Builder
	for _, path := range git.untracked {
		output.WriteString(path)
		output.WriteByte(0)
	}
	return []byte(output.String())
}

func (git *scannerFixtureGit) combinedOutput() []byte {
	objectIDLength := 40
	if git.objectFormat == "sha256" {
		objectIDLength = 64
	}
	objectID := strings.Repeat("a", objectIDLength)
	var output strings.Builder
	for _, entry := range git.tracked {
		output.WriteString("H ")
		output.WriteString(entry.Mode)
		output.WriteByte(' ')
		output.WriteString(objectID)
		output.WriteString(" 0\t")
		output.WriteString(entry.Path)
		output.WriteByte(0)
	}
	for _, path := range git.untracked {
		output.WriteString("? ")
		output.WriteString(path)
		output.WriteByte(0)
	}
	return []byte(output.String())
}

func (git *scannerFixtureGit) statusOutput() []byte {
	branchOID := "(initial)"
	if git.hasHeadOID {
		branchOID = git.headOID
	}
	branchHead := "(detached)"
	if git.hasRefLabel {
		branchHead = git.refLabel
	}
	records := []string{"# branch.oid " + branchOID, "# branch.head " + branchHead}
	if git.dirty {
		records = append(records, "1 M. N... 100644 100644 100644 aaaaaaaa bbbbbbbb dirty.go")
	}
	for _, path := range git.untracked {
		records = append(records, "? "+path)
	}
	return []byte(strings.Join(records, "\x00") + "\x00")
}

func scannerGitOutput(value string) GitResult {
	return GitResult{Stdout: []byte(value + "\n")}
}

func scannerGitOperation(args []string) string {
	if len(args) < 3 || args[0] != "-C" {
		return ""
	}
	command := append([]string(nil), args[2:]...)
	if len(command) > 0 && command[len(command)-1] == "--" {
		command = command[:len(command)-1]
	}
	return strings.Join(command, " ")
}

type scannerFixtureFiles struct {
	denyLstat    map[string]error
	denyRead     map[string]error
	modeOverride map[string]fs.FileMode
	reparse      map[string]bool
	readCalls    map[string]int
	changing     map[string]bool
}

func (files *scannerFixtureFiles) Lstat(path string) (ScannerFileInfo, error) {
	path = filepath.Clean(path)
	if err, found := files.denyLstat[path]; found {
		return ScannerFileInfo{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return ScannerFileInfo{}, err
	}
	mode := info.Mode()
	if override, found := files.modeOverride[path]; found {
		mode = override
	}
	return ScannerFileInfo{
		Mode:         mode,
		Size:         info.Size(),
		ModTime:      info.ModTime(),
		ReparsePoint: files.reparse[path],
	}, nil
}

func (files *scannerFixtureFiles) ReadFile(path string) ([]byte, error) {
	path = filepath.Clean(path)
	files.readCalls[path]++
	if err, found := files.denyRead[path]; found {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if files.changing[path] {
		next := []byte("changing-" + strings.Repeat("x", files.readCalls[path]+1))
		if err := os.WriteFile(path, next, 0o600); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func (files *scannerFixtureFiles) setMode(path string, mode fs.FileMode) {
	files.modeOverride[filepath.Clean(path)] = mode
}

func (files *scannerFixtureFiles) markReparse(path string) {
	files.reparse[filepath.Clean(path)] = true
}

func (files *scannerFixtureFiles) markChanging(path string) {
	files.changing[filepath.Clean(path)] = true
}

func (files *scannerFixtureFiles) readCount(path string) int {
	return files.readCalls[filepath.Clean(path)]
}

type scannerFixtureSecretDetector struct {
	marker []byte
}

func (detector scannerFixtureSecretDetector) ContainsSecret(_ string, body []byte) bool {
	return bytes.Contains(body, detector.marker)
}

func scannerAssertCensus(t *testing.T, result ScannerResult, outcome IndexScanOutcome, complete, canDeleteAll bool) {
	t.Helper()
	if got := result.Census.Outcome; got != outcome {
		t.Fatalf("census.outcome = %q, want %q", got, outcome)
	}
	if got := result.Census.Complete; got != complete {
		t.Fatalf("census.complete = %t, want %t", got, complete)
	}
	if got := result.Census.CanDeleteAll; got != canDeleteAll {
		t.Fatalf("census.can_delete_all = %t, want %t", got, canDeleteAll)
	}
}

func scannerAssertFile(t *testing.T, result ScannerResult, path string, state IndexFileState, exclusion ScannerExclusion, body []byte) {
	t.Helper()
	file := scannerFile(t, result, path)
	if got := file.State; got != state {
		t.Fatalf("file %q state = %q, want %q", path, got, state)
	}
	if got := file.Exclusion; got != exclusion {
		t.Fatalf("file %q exclusion = %q, want %q", path, got, exclusion)
	}
	if body == nil {
		if file.Body != nil {
			t.Fatalf("file %q body = %q, want nil", path, file.Body)
		}
		return
	}
	if !bytes.Equal(file.Body, body) {
		t.Fatalf("file %q body = %q, want %q", path, file.Body, body)
	}
}

func scannerFile(t *testing.T, result ScannerResult, path string) ScannerFile {
	t.Helper()
	for _, file := range result.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("scanner result omitted file %q: %#v", path, result.Files)
	return ScannerFile{}
}

func scannerAssertFileAbsent(t *testing.T, result ScannerResult, path string) {
	t.Helper()
	for _, file := range result.Files {
		if file.Path == path {
			t.Fatalf("scanner result unexpectedly included %q: %#v", path, file)
		}
	}
}

func scannerAssertOptionalString(t *testing.T, got *string, want string, present bool, field string) {
	t.Helper()
	if !present {
		if got != nil {
			t.Fatalf("%s = %q, want nil", field, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s = nil, want %q", field, want)
	}
	if *got != want {
		t.Fatalf("%s = %q, want %q", field, *got, want)
	}
}

func scannerAssertGitCallCount(t *testing.T, git *scannerFixtureGit, want int) {
	t.Helper()
	if got := len(git.calls); got != want {
		t.Fatalf("Git process count = %d, want %d; calls=%#v", got, want, git.calls)
	}
}

func scannerAssertGitOperations(t *testing.T, git *scannerFixtureGit, root string, start int, operations ...string) {
	t.Helper()
	scannerAssertGitCallCount(t, git, start+len(operations))
	for index, want := range operations {
		call := git.calls[start+index]
		if len(call.Args) < 3 || call.Args[0] != "-C" || call.Args[1] != root {
			t.Fatalf("Git invocation is not an argument-vector root-bound call: %#v", call.Args)
		}
		if !scannerHasEnvironment(call.Env, "GIT_OPTIONAL_LOCKS", "0") || !scannerHasEnvironment(call.Env, "GIT_TERMINAL_PROMPT", "0") {
			t.Fatalf("Git invocation omitted required read-only environment: %#v", call.Env)
		}
		if got := scannerGitOperation(call.Args); got != want {
			t.Fatalf("Git operation = %q, want %q", got, want)
		}
	}
}

func scannerAssertGitPlumbing(t *testing.T, git *scannerFixtureGit, root string) {
	t.Helper()
	required := []string{
		"rev-parse --show-toplevel --absolute-git-dir --git-common-dir --git-path HEAD --show-object-format",
		"status --porcelain=v2 --branch -z --untracked-files=all",
		"ls-files --stage --others --exclude-standard -t -z",
	}
	allowed := make(map[string]bool, len(required))
	for _, operation := range required {
		allowed[operation] = true
	}
	seen := make(map[string]bool, len(required))
	for _, call := range git.calls {
		if len(call.Args) < 3 || call.Args[0] != "-C" || call.Args[1] != root {
			t.Fatalf("Git invocation is not an argument-vector root-bound call: %#v", call.Args)
		}
		if !scannerHasEnvironment(call.Env, "GIT_OPTIONAL_LOCKS", "0") {
			t.Fatalf("Git invocation omitted GIT_OPTIONAL_LOCKS=0: %#v", call.Env)
		}
		if !scannerHasEnvironment(call.Env, "GIT_TERMINAL_PROMPT", "0") {
			t.Fatalf("Git invocation omitted GIT_TERMINAL_PROMPT=0: %#v", call.Env)
		}
		operation := scannerGitOperation(call.Args)
		if !allowed[operation] {
			t.Fatalf("Git invocation is not approved read-only plumbing: %#v", call.Args)
		}
		seen[operation] = true
	}
	if len(git.calls) != len(required) {
		t.Fatalf("Git census calls = %d, want collapsed %d", len(git.calls), len(required))
	}
	for _, operation := range required {
		if !seen[operation] {
			t.Fatalf("scanner omitted required Git plumbing %q; calls=%#v", operation, git.calls)
		}
	}
}

func scannerHasEnvironment(environment []string, key, value string) bool {
	wanted := key + "=" + value
	for _, entry := range environment {
		if entry == wanted {
			return true
		}
	}
	return false
}
