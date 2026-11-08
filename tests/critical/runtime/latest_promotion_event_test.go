//go:build critical

package runtime_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLatestPromotionEventAcceptance(t *testing.T) {
	var workflow struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Jobs struct {
			PromoteLatest struct {
				If    string `yaml:"if"`
				Steps []struct {
					Name string `yaml:"name"`
					Run  string `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"promote-latest"`
		} `yaml:"jobs"`
	}
	path := filepath.Join(repositoryRoot(t), ".github", "workflows", "promote-latest-release-images.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.On.WorkflowRun.Workflows) != 1 || workflow.On.WorkflowRun.Workflows[0] != "Docker Publish" || len(workflow.On.WorkflowRun.Types) != 1 || workflow.On.WorkflowRun.Types[0] != "completed" {
		t.Fatal("promoter must receive only completed Docker Publish workflow runs")
	}
	selector := workflow.Jobs.PromoteLatest.If
	if selector == "" {
		t.Fatal("promoter job condition is missing")
	}
	for _, tc := range []struct {
		name, conclusion, workflowName, upstreamEvent string
		want                                          bool
	}{
		{"published images", "success", "Docker Publish", "workflow_run", true},
		{"failed publish", "failure", "Docker Publish", "workflow_run", false},
		{"foreign workflow", "success", "Release", "workflow_run", false},
		{"foreign upstream event", "success", "Docker Publish", "push", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github := map[string]any{"event_name": "workflow_run", "event": map[string]any{"workflow_run": map[string]any{"conclusion": tc.conclusion, "name": tc.workflowName, "event": tc.upstreamEvent}}}
			payload, err := json.Marshal(github)
			if err != nil {
				t.Fatal(err)
			}
			// This job condition uses only string equality and Boolean operators, whose
			// evaluation on these string-valued events matches GitHub expressions.
			cmd := exec.Command("node", "-e", `const vm = require('node:vm'); console.log(vm.runInNewContext(process.argv[1], {github: JSON.parse(process.argv[2])}));`, selector, string(payload))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("evaluate actual job condition: %v: %s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != fmt.Sprint(tc.want) {
				t.Fatalf("actual job condition for upstream %s (%s, %s) = %s, want %v", tc.upstreamEvent, tc.workflowName, tc.conclusion, got, tc.want)
			}
		})
	}

	var resolve string
	for _, step := range workflow.Jobs.PromoteLatest.Steps {
		if step.Name == "Resolve the official GitHub Release" {
			resolve = step.Run
		}
	}
	if resolve == "" {
		t.Fatal("release resolution step is missing")
	}
	const releaseHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const definitionHead = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	repo := repositoryRoot(t)
	for _, tc := range []struct {
		name, event string
		wantSuccess bool
	}{
		{"valid different definition head", "workflow_run", true},
		{"missing artifact", "workflow_run", false},
		{"expired artifact", "workflow_run", false},
		{"duplicate artifact", "workflow_run", false},
		{"wrong repository", "workflow_run", false},
		{"archive digest mismatch", "workflow_run", false},
		{"wrong artifact run", "workflow_run", false},
		{"wrong artifact attempt", "workflow_run", false},
		{"invalid evidence", "workflow_run", false},
		{"mismatched release", "workflow_run", false},
		{"failed publisher", "workflow_run", false},
		{"independent dispatch", "repository_dispatch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			evidence := writePublicationEvidenceFixture(t, repo, releaseHead, "v6.49.4")
			census := readJSONMap(t, filepath.Join(evidence, "artifact-census.json"))
			census["artifact_name"] = "engram-release-payload-42-1"
			writeJSONAt(t, filepath.Join(evidence, "artifact-census.json"), census)
			if tc.name == "invalid evidence" {
				if err := os.WriteFile(filepath.Join(evidence, "postgres.trivy.sarif"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				runImageGate(t, repo, true, "-Mode", "ValidatePublicationEvidence", "-TrustedOutputRoot", publicationEvidenceTrustedRoot(evidence), "-EvidenceRoot", evidence, "-CredentialDirectoryPath", publicationEvidenceCredentialPath(evidence))
			}
			var archive bytes.Buffer
			zw := zip.NewWriter(&archive)
			entries, err := os.ReadDir(evidence)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(evidence, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				writer, err := zw.Create(entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(dir, "fixture.zip")
			if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			run := map[string]any{"id": 42, "run_attempt": 2, "name": "Docker Publish", "path": ".github/workflows/docker-publish.yml", "event": "workflow_run", "head_sha": definitionHead, "status": "completed", "conclusion": "success", "repository": map[string]any{"id": 99}, "head_repository": map[string]any{"id": 99}}
			job := map[string]any{"name": "publish-images", "run_id": 42, "run_attempt": 2, "status": "completed", "conclusion": "success"}
			artifact := map[string]any{"id": 123, "name": "engram-publication-evidence-42-2", "expired": false, "digest": "sha256:" + sha256Hex(archive.Bytes()), "workflow_run": map[string]any{"id": 42, "head_sha": definitionHead, "repository_id": 99, "head_repository_id": 99}}
			artifacts := []any{artifact}
			switch tc.name {
			case "missing artifact":
				artifacts = []any{}
			case "expired artifact":
				artifact["expired"] = true
			case "duplicate artifact":
				artifacts = append(artifacts, artifact)
			case "wrong repository":
				artifact["workflow_run"].(map[string]any)["repository_id"] = 98
			case "archive digest mismatch":
				artifact["digest"] = "sha256:" + strings.Repeat("f", 64)
			case "wrong artifact run":
				artifact["workflow_run"].(map[string]any)["id"] = 41
			case "wrong artifact attempt":
				artifact["name"] = "engram-publication-evidence-42-1"
			case "failed publisher":
				job["conclusion"] = "failure"
			}
			encode := func(v any) string {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			officialHead := releaseHead
			if tc.name == "mismatched release" {
				officialHead = definitionHead
			}
			script := `function gh {
    $global:LASTEXITCODE = 0
    if ($env:GITHUB_EVENT_NAME -eq 'repository_dispatch' -and $args -notcontains '.tag_name' -and $args -notcontains '.sha') { throw 'dispatch must not inspect publisher evidence' }
    $call = $args -join ' '
    if ($call -match '/jobs\?') { return $env:FIXTURE_JOBS }
    if ($call -match '/artifacts\?') { return $env:FIXTURE_ARTIFACTS }
    if ($call -match '/attempts/2$') { return $env:FIXTURE_RUN }
    if ($args -contains '.tag_name') { return 'v6.49.4' }
    if ($args -contains '.sha') { return $env:FIXTURE_RELEASE_HEAD }
    throw 'unexpected GitHub API call'
}
function Invoke-WebRequest {
    param($Uri, $Headers, $OutFile)
    if ($Uri -cne 'https://api.github.com/repos/thebtf/engram/actions/artifacts/123/zip') { throw 'unexpected artifact URI' }
    Copy-Item -LiteralPath $env:FIXTURE_ARCHIVE -Destination $OutFile
}
` + resolve
			file := filepath.Join(dir, "resolve.ps1")
			if err := os.WriteFile(file, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("pwsh", "-NoProfile", "-File", file)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "GITHUB_EVENT_NAME="+tc.event, "TRIGGERING_WORKFLOW_NAME=Docker Publish", "TRIGGERING_WORKFLOW_HEAD_SHA="+definitionHead, "TRIGGERING_WORKFLOW_RUN_ID=42", "TRIGGERING_WORKFLOW_RUN_ATTEMPT=2", "EVENT_REPOSITORY_ID=99", "REPOSITORY_NAME=thebtf/engram", "RECEIPT_DIR="+dir, "RUNNER_TEMP="+dir, "GITHUB_API_URL=https://api.github.com", "GH_TOKEN=fixture", "FIXTURE_ARCHIVE="+archivePath, "FIXTURE_RUN="+encode(run), "FIXTURE_JOBS="+encode([]any{map[string]any{"jobs": []any{job}}}), "FIXTURE_ARTIFACTS="+encode([]any{map[string]any{"artifacts": artifacts}}), "FIXTURE_RELEASE_HEAD="+officialHead)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("success=%v, want %v: %s", err == nil, tc.wantSuccess, output)
			}
			if !tc.wantSuccess {
				want := map[string]string{
					"missing artifact": "missing or ambiguous", "duplicate artifact": "missing or ambiguous", "wrong artifact attempt": "missing or ambiguous",
					"expired artifact": "metadata is invalid", "wrong artifact run": "metadata is invalid", "wrong repository": "metadata is invalid",
					"archive digest mismatch": "archive digest mismatch", "invalid evidence": "SARIF is not a zero-finding match",
					"mismatched release": "does not match validated publisher evidence", "failed publisher": "publish-images job was not completed successfully",
				}[tc.name]
				lines := strings.Split(string(output), "\n")
				for i, line := range lines {
					lines[i] = strings.TrimPrefix(strings.TrimSpace(line), "|")
				}
				normalized := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
				if !strings.Contains(normalized, want) {
					t.Fatalf("wrong rejection, want %q: %s", want, output)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "release.json")); (err == nil) != tc.wantSuccess {
				t.Fatalf("unexpected release receipt: %v", err)
			}
		})
	}
}
