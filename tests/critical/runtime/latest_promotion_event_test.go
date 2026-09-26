//go:build critical

package runtime_test

import (
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
	const staleHead = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		name, head  string
		wantSuccess bool
	}{
		{"matching release head", releaseHead, true},
		{"stale triggering head", staleHead, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Stub only GitHub's external API; execute the actual workflow step.
			script := fmt.Sprintf(`function gh {
    $global:LASTEXITCODE = 0
    if ($args -contains '--paginate') { return '{"jobs":[{"name":"publish-images","run_id":42,"status":"completed","conclusion":"success"}]}' }
    if ($args -contains '.tag_name') { return 'v6.49.4' }
    if ($args -contains '.sha') { return '%s' }
    throw 'unexpected GitHub API call'
}
%s`, releaseHead, resolve)
			file := filepath.Join(dir, "resolve.ps1")
			if err := os.WriteFile(file, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("pwsh", "-NoProfile", "-File", file)
			cmd.Env = append(os.Environ(), "GITHUB_EVENT_NAME=workflow_run", "TRIGGERING_WORKFLOW_NAME=Docker Publish", "TRIGGERING_WORKFLOW_HEAD_SHA="+tc.head, "TRIGGERING_WORKFLOW_RUN_ID=42", "REPOSITORY_NAME=thebtf/engram", "RECEIPT_DIR="+dir)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("release head %s: success=%v, want %v: %s", tc.head, err == nil, tc.wantSuccess, output)
			}
			if !tc.wantSuccess && !strings.Contains(string(output), "does not match triggering workflow_run head") {
				t.Fatalf("stale head rejected for unexpected reason: %s", output)
			}
		})
	}
}
