//go:build critical

package runtime_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOrdinaryCIExactProseClassification(t *testing.T) {
	var workflow struct {
		Jobs struct {
			Classify struct {
				Steps []struct {
					ID  string `yaml:"id"`
					If  string `yaml:"if"`
					Run string `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"classify"`
			Migrations struct {
				If string `yaml:"if"`
			} `yaml:"migrations"`
			Test struct {
				Needs    string `yaml:"needs"`
				Strategy struct {
					Matrix struct {
						OS string `yaml:"os"`
					} `yaml:"matrix"`
				} `yaml:"strategy"`
				Steps []struct {
					Name string `yaml:"name"`
					If   string `yaml:"if"`
				} `yaml:"steps"`
			} `yaml:"test"`
		} `yaml:"jobs"`
	}
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "test.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatal(err)
	}
	var classify string
	for _, step := range workflow.Jobs.Classify.Steps {
		if step.ID == "diff" {
			if step.If != "github.event_name != 'workflow_dispatch'" {
				t.Fatal("manual dispatch must not classify prose")
			}
			classify = step.Run
		}
	}
	if classify == "" || workflow.Jobs.Test.Needs != "classify" {
		t.Fatal("ordinary test job must consume the real classifier")
	}
	if workflow.Jobs.Migrations.If != "github.event_name == 'workflow_dispatch'" || !strings.Contains(workflow.Jobs.Test.Strategy.Matrix.OS, `"windows-latest", "macos-14"`) {
		t.Fatal("manual frozen matrix or clean-DB migration gate changed")
	}
	for _, step := range workflow.Jobs.Test.Steps {
		if step.Name != "Checkout" && !strings.Contains(step.If, "needs.classify.outputs.prose_only != 'true'") {
			t.Fatalf("expensive %q step runs on exact prose-only change", step.Name)
		}
	}

	for _, tc := range []struct {
		name, event string
		changed     []string
		want        bool
	}{
		{"prose-only PR", "pull_request", []string{"AGENTS.md", "docs/RELEASE-PROTOCOL.md", "CHANGELOG.md"}, true},
		{"prose-only push", "push", []string{"CHANGELOG.md"}, true},
		{"mixed PR", "pull_request", []string{"AGENTS.md", "internal/version/version.go"}, false},
		{"runtime push", "push", []string{"internal/version/version.go"}, false},
		{"workflow PR", "pull_request", []string{".github/workflows/test.yml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			git("init", "--quiet")
			git("config", "user.name", "CI Test")
			git("config", "user.email", "ci@example.invalid")
			for _, path := range []string{"AGENTS.md", "docs/RELEASE-PROTOCOL.md", "CHANGELOG.md", "internal/version/version.go", ".github/workflows/test.yml"} {
				destination := filepath.Join(repo, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination, []byte("base\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", "-A")
			git("commit", "--quiet", "-m", "baseline")
			base := git("rev-parse", "HEAD")
			for _, path := range tc.changed {
				if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", "-A")
			git("commit", "--quiet", "-m", "candidate")
			head := git("rev-parse", "HEAD")
			outputPath := filepath.Join(repo, "ci-output")
			cmd := exec.Command("pwsh", "-NoProfile", "-Command", classify)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "EVENT_NAME="+tc.event, "BASE_SHA="+base, "HEAD_SHA="+head, "GITHUB_OUTPUT="+outputPath)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("actual classifier failed: %v: %s", err, output)
			}
			result, err := os.ReadFile(outputPath)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if got := strings.Contains(string(result), "prose_only=true"); got != tc.want {
				t.Fatalf("actual classifier: got prose_only=%v, want %v, output=%s", got, tc.want, output)
			}
		})
	}
}
