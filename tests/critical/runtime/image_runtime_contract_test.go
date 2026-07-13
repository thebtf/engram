//go:build critical

// Package runtime_test contains @critical image tests. These tests use real
// Docker containers and the shipped HTTP surfaces; they are not unit mocks.
package runtime_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	defaultServerImage   = "engram:prc-server"
	defaultOperatorImage = "engram:prc-operator-console"
	defaultPostgresImage = "engram:prc-postgres"
)

type imageInspect struct {
	ID     string `json:"Id"`
	Config struct {
		User        string              `json:"User"`
		Env         []string            `json:"Env"`
		Entrypoint  []string            `json:"Entrypoint"`
		Cmd         []string            `json:"Cmd"`
		Labels      map[string]string   `json:"Labels"`
		Healthcheck *dockerHealthConfig `json:"Healthcheck"`
	} `json:"Config"`
}

type dockerHealthConfig struct {
	Test []string `json:"Test"`
}

type containerInspect struct {
	Config struct {
		User string `json:"User"`
	} `json:"Config"`
	HostConfig struct {
		ReadonlyRootfs bool     `json:"ReadonlyRootfs"`
		CapDrop        []string `json:"CapDrop"`
		SecurityOpt    []string `json:"SecurityOpt"`
	} `json:"HostConfig"`
}

type stackFixture struct {
	prefix         string
	network        string
	postgresVolume string
	serverVolume   string
	postgres       string
	server         string
}

// @critical
// @category: contract
// @features: [image-remediation, release-safety]
func TestDockerReleaseRefFreshnessGuard(t *testing.T) {
	verifyDockerReleaseRefFreshnessGuard(t, repositoryRoot(t))
}

// @critical
// @category: behavioral
// @features: [image-remediation, operator-console]
// @dev_stand: required
func TestOperatorConsoleRuntimeTargetContract(t *testing.T) {
	repo := repositoryRoot(t)
	requireFileContains(t, filepath.Join(repo, "Dockerfile"),
		"gcr.io/distroless/nodejs22-debian13@sha256:773a62fbe24a3f8c8b24b16fd59154627f8b406737bc906f83bf1732bc8907dd",
		"NUXT_OPERATOR_API_TARGET=http://server:37777",
		"CMD [\".output/server/index.mjs\"]",
		"http://127.0.0.1:3000/api/ready",
	)
	requireFileContains(t, filepath.Join(repo, ".dockerignore"),
		".env",
		".env.*",
		"*.pem",
		"*.key",
		".npmrc",
		"secrets/",
	)
	requireFileContains(t, filepath.Join(repo, "deploy", "docker-compose.runtime.yml"),
		"operator-console:",
		"${ENGRAM_OPERATOR_IMAGE:?set ENGRAM_OPERATOR_IMAGE from the immutable release manifest}",
		"NUXT_OPERATOR_API_TARGET: \"http://server:37777\"",
	)
	requireFileNotContains(t, filepath.Join(repo, "deploy", "docker-compose.runtime.yml"),
		"operator-web:", "NUXT_ENGRAM_API_TARGET")

	operatorImage := imageFromEnv("ENGRAM_OPERATOR_IMAGE", defaultOperatorImage)
	operatorConfig := inspectImage(t, operatorImage)
	if operatorConfig.Config.User != "65532" && operatorConfig.Config.User != "65532:65532" {
		t.Fatalf("operator image must run as UID 65532, got %q", operatorConfig.Config.User)
	}
	if got := strings.Join(operatorConfig.Config.Entrypoint, " "); got != "/nodejs/bin/node" {
		t.Fatalf("operator must retain the distroless node entrypoint, got %q", got)
	}
	if got := strings.Join(operatorConfig.Config.Cmd, " "); got != ".output/server/index.mjs" {
		t.Fatalf("operator command mismatch: %q", got)
	}
	requireEnv(t, operatorConfig.Config.Env, "NUXT_OPERATOR_API_TARGET", "http://server:37777")
	requireProvenanceLabels(t, operatorConfig.Config.Labels)
	requireHealthCommand(t, operatorConfig.Config.Healthcheck, "/usr/local/bin/engram-healthcheck", "http://127.0.0.1:3000/api/ready")

	stack := startServerStack(t)
	operator := stack.prefix + "-operator"
	t.Cleanup(func() { removeContainer(operator) })
	runDocker(t, nil,
		"run", "-d", "--name", operator,
		"--network", stack.network, "--network-alias", "operator-console",
		"-p", "127.0.0.1::3000",
		"--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,uid=65532,gid=65532,mode=0700,size=64m",
		"-e", "NUXT_OPERATOR_API_TARGET=http://server:37777",
		operatorImage,
	)
	waitHealthy(t, operator, 90*time.Second)
	requireHardenedContainer(t, operator, "65532:65532")

	baseURL := mappedURL(t, operator, "3000/tcp")
	root := requireHTTP(t, baseURL+"/", http.StatusOK)
	if !bytes.Contains(root, []byte("_nuxt/")) {
		t.Fatal("operator root did not reference generated Nuxt assets")
	}
	ready := requireHTTP(t, baseURL+"/api/ready", http.StatusOK)
	if strings.TrimSpace(string(ready)) != `{"status":"ready"}` {
		t.Fatalf("operator proxy did not return exact backend readiness: %q", ready)
	}
	health := requireHTTP(t, baseURL+"/api/health", http.StatusOK)
	if !bytes.Contains(health, []byte(`"status":"ready"`)) {
		t.Fatalf("operator proxy did not reach the ready backend health route: %q", health)
	}
	requireReferencedAsset(t, baseURL, root)

	runDocker(t, nil, "restart", operator)
	baseURL = mappedURL(t, operator, "3000/tcp")
	ready = requireHTTPEventually(t, baseURL+"/api/ready", http.StatusOK, 60*time.Second)
	waitHealthy(t, operator, 90*time.Second)
	if strings.TrimSpace(string(ready)) != `{"status":"ready"}` {
		t.Fatalf("operator lost backend readiness after restart: %q", ready)
	}

	wrongTarget := stack.prefix + "-operator-wrong-target"
	t.Cleanup(func() { removeContainer(wrongTarget) })
	runOperatorFixture(t, wrongTarget, stack.network,
		"-e", "NUXT_OPERATOR_API_TARGET=http://missing-backend:37777",
		"-e", "NUXT_ENGRAM_API_TARGET=http://server:37777",
	)
	waitNotHealthy(t, wrongTarget, 20*time.Second)

	missingTarget := stack.prefix + "-operator-missing-target"
	t.Cleanup(func() { removeContainer(missingTarget) })
	runOperatorFixture(t, missingTarget, "none",
		"-e", "NUXT_OPERATOR_API_TARGET=",
	)
	waitNotHealthy(t, missingTarget, 20*time.Second)

	for _, fixture := range []struct {
		name     string
		response string
	}{
		{name: "malformed", response: `res.writeHead(200,{'content-type':'application/json'});res.end('not-json')`},
		{name: "error-200", response: `res.writeHead(200,{'content-type':'application/json'});res.end('{"status":"error"}')`},
		{name: "root-only", response: `if(req.url==='/'){res.writeHead(200,{'content-type':'application/json'});res.end('{"status":"ready"}')}else{res.writeHead(404);res.end('missing')}`},
		{name: "timeout", response: `if(req.url!=='/api/ready'){res.writeHead(404);res.end('missing')}`},
	} {
		t.Run("backend "+fixture.name+" never becomes healthy", func(t *testing.T) {
			backend := stack.prefix + "-backend-" + fixture.name
			operator := stack.prefix + "-operator-" + fixture.name
			t.Cleanup(func() {
				removeContainer(operator)
				removeContainer(backend)
			})
			startFakeNodeBackend(t, backend, stack.network, fixture.response)
			runOperatorFixture(t, operator, stack.network,
				"-e", "NUXT_OPERATOR_API_TARGET=http://"+backend+":37777",
			)
			waitNotHealthy(t, operator, 20*time.Second)
		})
	}
}

// @critical
// @category: behavioral
// @features: [image-remediation]
// @dev_stand: required
func TestServerImageContract(t *testing.T) {
	repo := repositoryRoot(t)
	t.Run("release ref freshness guard", func(t *testing.T) {
		verifyDockerReleaseRefFreshnessGuard(t, repo)
	})
	requireFileContains(t, filepath.Join(repo, "Dockerfile"),
		"gcr.io/distroless/base-debian13@sha256:b78832f41c8128046807c24840ebee4f1c18ba7870eed423d8750c272c15e147",
		"HOME=/var/lib/engram",
		"http://127.0.0.1:37777/api/ready",
		"VERSION must be canonical SemVer or sha-<40 lowercase hex>",
		"Numeric prerelease identifiers",
	)
	requireFileNotContains(t, filepath.Join(repo, "Dockerfile"), "curl -f http://localhost:37777/health")

	serverImage := imageFromEnv("ENGRAM_SERVER_IMAGE", defaultServerImage)
	serverConfig := inspectImage(t, serverImage)
	if serverConfig.Config.User != "65532" && serverConfig.Config.User != "65532:65532" {
		t.Fatalf("server image must run as UID 65532, got %q", serverConfig.Config.User)
	}
	requireEnv(t, serverConfig.Config.Env, "HOME", "/var/lib/engram")
	requireProvenanceLabels(t, serverConfig.Config.Labels)
	requireHealthCommand(t, serverConfig.Config.Healthcheck, "/usr/local/bin/engram-healthcheck", "http://127.0.0.1:37777/api/ready")

	stack := startServerStack(t)
	requireHardenedContainer(t, stack.server, "65532:65532")
	inspectionImage := imageFromEnv("ENGRAM_POSTGRES_IMAGE", defaultPostgresImage)
	requireVolumeMetadata(t, stack.serverVolume, inspectionImage, "65532:65532:700")
	requireVolumePathMetadata(t, stack.serverVolume, inspectionImage, "/data/.engram", "65532:65532:700")
	requireVolumePathMetadata(t, stack.serverVolume, inspectionImage, "/data/.engram/settings.json", "65532:65532:600")
	settingsBeforeRestart := readVolumeFile(t, stack.serverVolume, inspectionImage, "/data/.engram/settings.json")
	baseURL := mappedURL(t, stack.server, "37777/tcp")
	health := requireHTTP(t, baseURL+"/health", http.StatusOK)
	if !bytes.Contains(health, []byte(`"status":"ready"`)) {
		t.Fatalf("liveness did not report ready after initialization: %q", health)
	}
	ready := requireHTTP(t, baseURL+"/api/ready", http.StatusOK)
	if strings.TrimSpace(string(ready)) != `{"status":"ready"}` {
		t.Fatalf("semantic readiness mismatch: %q", ready)
	}

	runDocker(t, nil, "restart", stack.server)
	baseURL = mappedURL(t, stack.server, "37777/tcp")
	ready = requireHTTPEventually(t, baseURL+"/api/ready", http.StatusOK, 60*time.Second)
	waitHealthy(t, stack.server, 90*time.Second)
	if strings.TrimSpace(string(ready)) != `{"status":"ready"}` {
		t.Fatalf("server did not regain readiness after restart: %q", ready)
	}
	settingsAfterRestart := readVolumeFile(t, stack.serverVolume, inspectionImage, "/data/.engram/settings.json")
	if !bytes.Equal(settingsBeforeRestart, settingsAfterRestart) {
		t.Fatal("server settings file changed or disappeared across container restart")
	}

	failedServer := stack.prefix + "-server-init-failure"
	failedHome := stack.prefix + "-server-failed-home"
	runDocker(t, nil, "volume", "create", failedHome)
	t.Cleanup(func() {
		removeContainer(failedServer)
		removeVolume(failedHome)
	})
	runDocker(t, nil,
		"run", "-d", "--name", failedServer,
		"-p", "127.0.0.1::37777",
		"--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--health-start-period", "1ms", "--health-interval", "1s", "--health-timeout", "4s", "--health-retries", "2",
		"-v", failedHome+":/var/lib/engram",
		"-e", "HOME=/var/lib/engram",
		"-e", "DATABASE_DSN=postgres://engram:engram@127.0.0.1:1/unreachable?sslmode=disable",
		"-e", "ENGRAM_AUTH_DISABLED=true",
		serverImage,
	)
	waitNotHealthy(t, failedServer, 60*time.Second)
	failedURL := mappedURL(t, failedServer, "37777/tcp")
	failedHealth := requireHTTP(t, failedURL+"/health", http.StatusOK)
	if !bytes.Contains(failedHealth, []byte(`"status":"error"`)) {
		t.Fatalf("failed initialization liveness must remain 200/error, got %q", failedHealth)
	}

	for _, fixture := range []struct {
		name    string
		prepare func(*testing.T, string)
		extra   []string
	}{
		{name: "absent-volume"},
		{name: "empty-home", extra: []string{"-e", "HOME="}},
		{name: "root-owned-volume", prepare: func(t *testing.T, volume string) {
			runDocker(t, nil, "volume", "create", volume)
			postgresImage := imageFromEnv("ENGRAM_POSTGRES_IMAGE", defaultPostgresImage)
			runDocker(t, nil,
				"run", "--rm", "--user", "0", "--entrypoint", "/bin/sh",
				"-v", volume+":/data", postgresImage,
				"-c", "touch /data/.unwritable-contract && chown 0:0 /data /data/.unwritable-contract && chmod 0500 /data",
			)
		}},
	} {
		t.Run(fixture.name+" fails closed", func(t *testing.T) {
			name := stack.prefix + "-server-" + fixture.name
			volume := stack.prefix + "-home-" + fixture.name
			t.Cleanup(func() {
				removeContainer(name)
				removeVolume(volume)
			})
			args := []string{
				"run", "-d", "--name", name,
				"--network", stack.network,
				"--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
				"--security-opt", "no-new-privileges:true",
				"--health-start-period", "1ms", "--health-interval", "1s", "--health-timeout", "4s", "--health-retries", "2",
				"-e", "DATABASE_DSN=postgres://engram:engram@postgres:5432/engram?sslmode=disable",
				"-e", "ENGRAM_AUTH_DISABLED=true",
			}
			if fixture.prepare != nil {
				fixture.prepare(t, volume)
				args = append(args, "-v", volume+":/var/lib/engram")
			}
			args = append(args, fixture.extra...)
			args = append(args, serverImage)
			runDocker(t, nil, args...)
			waitNotHealthy(t, name, 20*time.Second)
		})
	}
}

func verifyDockerReleaseRefFreshnessGuard(t *testing.T, repo string) {
	t.Helper()
	for _, composePath := range []string{
		filepath.Join(repo, "docker-compose.yml"),
		filepath.Join(repo, "deploy", "docker-compose.runtime.yml"),
	} {
		compose := readFile(t, composePath)
		for _, required := range []string{
			"${ENGRAM_SERVER_IMAGE:?", "${ENGRAM_OPERATOR_IMAGE:?", "${ENGRAM_POSTGRES_IMAGE:?",
		} {
			if !strings.Contains(compose, required) {
				t.Fatalf("%s does not require immutable release-manifest identity %q", composePath, required)
			}
		}
		for _, forbidden := range []string{":main", ":latest", "ghcr.io/thebtf/engram:"} {
			if strings.Contains(compose, forbidden) {
				t.Fatalf("%s retains moving image default %q", composePath, forbidden)
			}
		}
	}
	verificationPath := filepath.Join(repo, ".github", "workflows", "docker.yaml")
	publisherPath := filepath.Join(repo, ".github", "workflows", "docker-publish.yml")
	for _, workflowPath := range []string{verificationPath, publisherPath} {
		content, err := os.ReadFile(workflowPath)
		if err != nil {
			t.Fatal(err)
		}
		workflow := string(content)
		if regexp.MustCompile(`(?m)^  packages:\s*write\s*$`).MatchString(workflow) {
			t.Fatalf("%s grants packages:write at workflow scope", workflowPath)
		}
		for _, body := range inlineRunBodies(workflow) {
			for _, forbidden := range []string{
				`${{ github.`, `${{ steps.`, `${{ inputs.`, "git describe", "type=ref", "type=semver",
			} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s embeds untrusted or alias-generating input in inline run body: %q", workflowPath, forbidden)
				}
			}
		}
		for _, forbidden := range []string{
			"type=ref", "type=semver", "type=sha", "is_default_branch", "{{major}}", "{{minor}}",
		} {
			if strings.Contains(workflow, forbidden) {
				t.Fatalf("%s retains moving metadata alias %q", workflowPath, forbidden)
			}
		}
		for _, match := range regexp.MustCompile(`(?m)^\s*uses:\s*([^\s#]+)`).FindAllStringSubmatch(workflow, -1) {
			parts := strings.Split(match[1], "@")
			if len(parts) != 2 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(parts[1]) {
				t.Fatalf("%s contains an unpinned action reference %q", workflowPath, match[1])
			}
		}
	}

	const scoutVersion = "1.23.1"
	const scoutArchiveSHA256 = "0f778f9d833f28bc6cccff95e33039849c0afcecafa38d9f46fe74bfd0915714"
	for _, workflowPath := range []string{verificationPath, publisherPath} {
		workflow := readFile(t, workflowPath)
		for _, required := range []string{
			"docker-scout_" + scoutVersion + "_linux_amd64.tar.gz",
			scoutArchiveSHA256,
			".docker/cli-plugins/docker-scout",
			"docker scout version",
		} {
			if !strings.Contains(workflow, required) {
				t.Fatalf("%s does not provision the pinned Docker Scout CLI contract %q", workflowPath, required)
			}
		}
	}

	imageGate := readFile(t, filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1"))
	for _, required := range []string{
		"ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE",
		"ENGRAM_DATABASE_DSN_SECRET_FILE",
		"ENGRAM_POSTGRES_PASSWORD_SECRET_FILE",
		"ENGRAM_VAULT_KEY_SECRET_FILE",
		"engram-image-secrets-",
		"PendingImageSecretRoot",
		"Set-ComposeSecretPathAccess",
		"secret_files_cleaned",
	} {
		if !strings.Contains(imageGate, required) {
			t.Fatalf("image gate does not own the temporary secret-file lifecycle %q", required)
		}
	}

	devStand := readFile(t, filepath.Join(repo, "scripts", "production-gates", "run-dev-stand.ps1"))
	for _, required := range []string{"engram-dev-stand-secrets-", "PendingDevStandSecretRoot", "Set-ComposeSecretPathAccess", "-DevStandSecretRoot", "secret_files_cleaned"} {
		if !strings.Contains(devStand, required) {
			t.Fatalf("dev-stand lifecycle does not preserve and clean its secret root %q", required)
		}
	}
	devStandAction := readFile(t, filepath.Join(repo, "scripts", "production-gates", "run-db-suite.ps1"))
	for _, required := range []string{
		"ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE",
		"ENGRAM_DATABASE_DSN_SECRET_FILE",
		"ENGRAM_POSTGRES_PASSWORD_SECRET_FILE",
		"ENGRAM_VAULT_KEY_SECRET_FILE",
		"DevStandSecretRoot",
		"Set-ComposeSecretPathAccess",
		"ghcr.io/thebtf/engram-postgres:main",
		"postgres_build_completed",
		"dev-stand-failure-logs",
		"credential_secret_mounts_verified",
	} {
		if !strings.Contains(devStandAction, required) {
			t.Fatalf("dev-stand action does not consume file-backed secrets safely %q", required)
		}
	}
	secretAccess := readFile(t, filepath.Join(repo, "scripts", "production-gates", "compose-secret-access.ps1"))
	for _, required := range []string{"Set-ComposeSecretPathAccess", "SetAccessRuleProtection", "LocalSystemSid", "UserExecute", "OtherRead"} {
		if !strings.Contains(secretAccess, required) {
			t.Fatalf("compose secret access helper lacks cross-platform host-private/container-readable control %q", required)
		}
	}
	secretAccessSelfTest := filepath.Join(repo, "scripts", "production-gates", "test-compose-secret-access.ps1")
	secretAccessCommand := exec.Command("pwsh", "-NoProfile", "-File", secretAccessSelfTest)
	secretAccessOutput, err := secretAccessCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("compose secret access self-test failed: %v\n%s", err, secretAccessOutput)
	}
	if !strings.Contains(string(secretAccessOutput), "compose-secret-access self-test=PASS") {
		t.Fatalf("compose secret access self-test emitted no PASS proof: %s", secretAccessOutput)
	}

	otlpGate := readFile(t, filepath.Join(repo, "scripts", "production-smoke", "verify-otlp.ps1"))
	for _, required := range []string{"process_residue_checked", "container_residue_checked", "Get-ResidualProcessSnapshot", "Get-ResidualContainerSnapshot", "Wait-ForResidualResources", "started_at_utc_ticks", "residue_poll_timed_out"} {
		if !strings.Contains(otlpGate, required) {
			t.Fatalf("OTLP evidence still lacks measured residue proof %q", required)
		}
	}
	for _, forbidden := range []string{"process_residue = @()", "container_residue = @()", "Start-Sleep -Milliseconds 300"} {
		if strings.Contains(otlpGate, forbidden) {
			t.Fatalf("OTLP evidence hard-codes a false residue claim %q", forbidden)
		}
	}

	t.Run("Nuxt UI credential-form advisory remains unreachable", func(t *testing.T) {
		verifyNuxtUICredentialFormAdvisoryUnreachable(t, repo)
	})
	t.Run("operator native optional tree is verified", func(t *testing.T) {
		verifyOperatorNativeOptionalTree(t, repo)
	})
	t.Run("operator compose build receives exact source version", func(t *testing.T) {
		verifyOperatorComposeBuildVersion(t, repo)
	})

	verification := readFile(t, verificationPath)
	for _, fragment := range []string{
		"branches: [main]",
		"tags: [\"v*\"]",
		"workflow_dispatch:",
		"verify-images:",
		"Mode BuildAndScan",
	} {
		if !strings.Contains(verification, fragment) {
			t.Fatalf("unprivileged Docker workflow lacks verification contract %q", fragment)
		}
	}
	for _, forbidden := range []string{"packages: write", "docker/login-action@", "Mode Publish", "Mode ValidateWorkflowRun"} {
		if strings.Contains(verification, forbidden) {
			t.Fatalf("unprivileged Docker workflow acquired publication authority %q", forbidden)
		}
	}

	publisher := readFile(t, publisherPath)
	for _, fragment := range []string{
		"workflow_run:",
		"workflows: [\"Docker\"]",
		"types: [completed]",
		"packages: write",
		"prepare-release:",
		"publish-images:",
		"needs: prepare-release",
		"ref: main",
		"Mode ValidateWorkflowRun",
		"Mode BuildAndScan",
		"Mode ValidateArtifactMetadata",
		"Mode ValidatePayload",
		"Mode LoadPayload",
		"Mode ValidatePublicationEvidence",
		"Mode PlanPublication",
		"Mode Publish",
		"-ReleasePayloadPath",
		"-RepositoryRoot ./trusted",
		"-RepositoryRoot ./candidate",
		"-TrustedOutputRoot $env:RUNNER_TEMP",
		"-ExpectedSha $env:VALIDATED_COMMIT",
		"artifact-id:",
		"artifact-digest:",
		"artifact-ids:",
		"artifact_name=engram-release-payload-$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT",
		"persist-credentials: false",
		"cancel-in-progress: false",
		"docker logout ghcr.io",
	} {
		if !strings.Contains(publisher, fragment) {
			t.Fatalf("trusted workflow_run publisher lacks contract %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"workflow_dispatch:", "./candidate/scripts/", "overwrite: true",
		"candidate/.agent/", "path: candidate/.agent", "ghcr.io/thebtf/engram:main", "value=latest",
	} {
		if strings.Contains(publisher, forbidden) {
			t.Fatalf("trusted publisher retains forbidden candidate-controlled or moving surface %q", forbidden)
		}
	}
	if got := strings.Count(publisher, "persist-credentials: false"); got != 3 {
		t.Fatalf("two-runner publisher must disable persisted checkout credentials three times, got %d", got)
	}
	for _, blankSecret := range []string{`GITHUB_TOKEN: ""`, `GH_TOKEN: ""`, `CR_PAT: ""`, `GHCR_TOKEN: ""`} {
		if !strings.Contains(publisher, blankSecret) {
			t.Fatalf("candidate build step does not explicitly clear %q", blankSecret)
		}
	}
	if got := strings.Count(publisher, "Mode ValidateWorkflowRun"); got != 2 {
		t.Fatalf("both runners must independently validate workflow_run provenance, got %d validators", got)
	}
	if got := strings.Count(publisher, "-EventOnlyValidation"); got != 1 {
		t.Fatalf("only the contents-read prepare job may use the event/git/ruleset validator, got %d", got)
	}
	prepareIndex := strings.Index(publisher, "prepare-release:")
	publishJobIndex := strings.Index(publisher, "publish-images:")
	packagesIndex := strings.Index(publisher, "packages: write")
	if !(prepareIndex >= 0 && prepareIndex < publishJobIndex && publishJobIndex < packagesIndex) {
		t.Fatalf("packages:write is not isolated to the fresh publish job: prepare=%d publish=%d packages=%d", prepareIndex, publishJobIndex, packagesIndex)
	}
	prepareJob := publisher[prepareIndex:publishJobIndex]
	for _, forbidden := range []string{"actions: read", "actions: write", "packages: write"} {
		if strings.Contains(prepareJob, forbidden) {
			t.Fatalf("prepare-release must remain contents:read-only, found %q", forbidden)
		}
	}
	privilegedJob := publisher[publishJobIndex:]
	for _, required := range []string{"contents: read", "actions: read", "packages: write"} {
		if !strings.Contains(privilegedJob, required) {
			t.Fatalf("fresh publisher lacks explicit least-privilege permission %q", required)
		}
	}
	if strings.Contains(publisher, "actions: write") {
		t.Fatal("release workflow must never grant actions:write")
	}
	for _, forbidden := range []string{"path: candidate", "./candidate", "Mode BuildAndScan", "go test", "docker compose"} {
		if strings.Contains(privilegedJob, forbidden) {
			t.Fatalf("fresh packages:write job executes or checks out candidate-controlled material %q", forbidden)
		}
	}
	validateIndex := strings.Index(publisher, "Mode ValidateWorkflowRun")
	candidateIndex := strings.Index(publisher, "Checkout exact validated candidate")
	buildIndex := strings.Index(publisher, "Mode BuildAndScan")
	uploadIndex := strings.Index(publisher, "actions/upload-artifact@")
	downloadIndex := strings.Index(publisher, "actions/download-artifact@")
	artifactValidationIndex := strings.Index(privilegedJob, "Mode ValidateArtifactMetadata") + publishJobIndex
	payloadValidationIndex := strings.Index(privilegedJob, "Mode ValidatePayload") + publishJobIndex
	loadIndex := strings.Index(privilegedJob, "Mode LoadPayload") + publishJobIndex
	preflightIndex := strings.Index(privilegedJob, "Mode PlanPublication") + publishJobIndex
	loginIndex := strings.Index(publisher, "docker/login-action@")
	publishIndex := strings.Index(publisher, "Mode Publish")
	logoutIndex := strings.Index(publisher, "docker logout ghcr.io")
	evidenceValidationIndex := strings.Index(privilegedJob, "Mode ValidatePublicationEvidence") + publishJobIndex
	evidenceUploadIndex := strings.LastIndex(publisher, "actions/upload-artifact@")
	if !(validateIndex >= 0 && validateIndex < candidateIndex && candidateIndex < buildIndex && buildIndex < uploadIndex && uploadIndex < publishJobIndex && publishJobIndex < artifactValidationIndex && artifactValidationIndex < downloadIndex && downloadIndex < payloadValidationIndex && payloadValidationIndex < loadIndex && loadIndex < preflightIndex && preflightIndex < loginIndex && loginIndex < publishIndex && publishIndex < logoutIndex && logoutIndex < evidenceValidationIndex && evidenceValidationIndex < evidenceUploadIndex) {
		t.Fatalf("two-runner trust/credential ordering is unsafe: validate=%d candidate=%d build=%d upload=%d publishJob=%d artifact=%d download=%d payload=%d load=%d preflight=%d login=%d publish=%d logout=%d evidenceValidate=%d evidenceUpload=%d", validateIndex, candidateIndex, buildIndex, uploadIndex, publishJobIndex, artifactValidationIndex, downloadIndex, payloadValidationIndex, loadIndex, preflightIndex, loginIndex, publishIndex, logoutIndex, evidenceValidationIndex, evidenceUploadIndex)
	}

	t.Run("repository controlled single writer", func(t *testing.T) {
		testRepositorySingleWriter(t, repo)
	})

	t.Run("canonical release version and hostile Git refs", func(t *testing.T) {
		testCanonicalReleaseValidation(t, repo)
	})
	t.Run("workflow_run provenance and protected-main authority matrix", func(t *testing.T) {
		testWorkflowRunTrustMatrix(t, repo)
	})
	t.Run("workflow_run publisher selector matrix", func(t *testing.T) {
		testWorkflowRunPublisherSelector(t, publisher, repo)
	})
	t.Run("same-run immutable artifact bridge matrix", func(t *testing.T) {
		testArtifactBridgeMatrix(t, repo)
	})
	t.Run("tag ruleset positive and negative matrix", func(t *testing.T) {
		testTagRulesetMatrix(t, repo)
	})
	t.Run("registry compare-before-write matrix", func(t *testing.T) {
		testRegistryCASMatrix(t, repo)
	})
	t.Run("movement before and after old guard", func(t *testing.T) {
		testImmutableTagMovement(t, repo)
	})
}

func testWorkflowRunPublisherSelector(t *testing.T, publisher, repo string) {
	t.Helper()
	const selector = "if: github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && startsWith(github.event.workflow_run.head_branch, 'v')"
	if !strings.Contains(strings.ReplaceAll(publisher, "\r\n", "\n"), selector) {
		t.Fatalf("publisher does not gate preflight with the exact successful push/tag-shaped selector %q", selector)
	}

	shouldEnterPreflight := func(conclusion, event, headBranch string) bool {
		return conclusion == "success" && event == "push" && strings.HasPrefix(headBranch, "v")
	}
	for _, test := range []struct {
		name                          string
		conclusion, event, headBranch string
		want                          bool
	}{
		{name: "successful pull request skips", conclusion: "success", event: "pull_request", headBranch: "feature/image", want: false},
		{name: "successful main push skips", conclusion: "success", event: "push", headBranch: "main", want: false},
		{name: "successful tag-shaped push enters", conclusion: "success", event: "push", headBranch: "v6.43.0", want: true},
		{name: "failed tag push skips", conclusion: "failure", event: "push", headBranch: "v6.43.0", want: false},
		{name: "cancelled tag push skips", conclusion: "cancelled", event: "push", headBranch: "v6.43.0", want: false},
		{name: "hostile pull request lookalike skips", conclusion: "success", event: "pull_request", headBranch: "v6.43.0", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldEnterPreflight(test.conclusion, test.event, test.headBranch); got != test.want {
				t.Fatalf("selector decision = %v, want %v", got, test.want)
			}
		})
	}

	t.Run("hostile tag-shaped push still fails closed in preflight", func(t *testing.T) {
		fixtureValue := workflowRunFixture(strings.Repeat("a", 40), "v1$(printf${IFS}INJECTED)")
		fixture := writeJSONFixture(t, fixtureValue)
		output := runImageGate(t, repo, false,
			"-Mode", "ValidateWorkflowRun",
			"-WorkflowRunFixturePath", fixture,
			"-Repository", "thebtf/engram")
		if strings.Contains(output, "value=v1INJECTED") {
			t.Fatalf("hostile workflow_run ref was evaluated as shell source: %s", output)
		}
	})
}

func testRepositorySingleWriter(t *testing.T, repo string) {
	t.Helper()
	allowedWorkflow := filepath.Clean(filepath.Join(repo, ".github", "workflows", "docker-publish.yml"))
	allowedScript := filepath.Clean(filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1"))
	if violations, err := repositorySingleWriterViolations(repo, allowedWorkflow, allowedScript); err != nil {
		t.Fatal(err)
	} else if len(violations) != 0 {
		for _, path := range violations {
			t.Errorf("executable surface %s contains an undeclared registry write credential or command", path)
		}
	}
	t.Run("quoted authority sentinel fails closed", func(t *testing.T) {
		testRepositorySingleWriterSentinel(t, repo, allowedWorkflow, allowedScript)
	})

	workflow := readFile(t, allowedWorkflow)
	if got := strings.Count(workflow, "packages: write"); got != 1 {
		t.Fatalf("release workflow must have exactly one packages:write grant, got %d", got)
	}
	if got := strings.Count(workflow, "docker/login-action@"); got != 1 {
		t.Fatalf("release workflow must have exactly one registry login seam, got %d", got)
	}
	if got := strings.Count(workflow, "-Mode Publish"); got != 1 {
		t.Fatalf("release workflow must invoke the sole publish mode exactly once, got %d", got)
	}
	if got := strings.Count(workflow, "packages: write"); got != 1 {
		t.Fatalf("packages:write must exist in exactly one fresh runner job, got %d", got)
	}
	if got := strings.Count(workflow, "actions/upload-artifact@"); got != 2 {
		t.Fatalf("release workflow must upload one bridge artifact and one post-logout evidence artifact, got %d", got)
	}
	if got := strings.Count(workflow, "actions/download-artifact@"); got != 1 {
		t.Fatalf("release workflow must download the bridge exactly once by artifact ID, got %d", got)
	}
	for _, forbidden := range []string{"PERSONAL_ACCESS_TOKEN", "PAT_TOKEN", "secrets."} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("release workflow accepts an external package credential route %q", forbidden)
		}
	}
	for _, cleared := range []string{`CR_PAT: ""`, `GHCR_TOKEN: ""`} {
		if !strings.Contains(workflow, cleared) {
			t.Fatalf("candidate execution environment does not clear package credential name %q", cleared)
		}
	}
	script := readFile(t, allowedScript)
	if got := strings.Count(script, "& docker push"); got != 1 {
		t.Fatalf("publication script must have exactly one controlled docker push seam, got %d", got)
	}
	if got := strings.Count(script, `'--build-arg', "VERSION=$buildVersion"`); got != 2 {
		t.Fatalf("both Dockerfile targets that traverse the shared builder must receive validated VERSION, got %d", got)
	}
	for _, required := range []string{
		"No registry write occurs until", "repository single-writer model", "external_package_admin_trust_boundary",
		"TrustedOutputRoot", "git-archive-tracked-files-only", "integration_id", "engram:cleanup-placeholder",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("publication script lacks single-writer trust-boundary contract %q", required)
		}
	}
}

func repositorySingleWriterViolations(repo, allowedWorkflow, allowedScript string) ([]string, error) {
	writePattern := regexp.MustCompile(`(?i)packages\s*:\s*["']?write\b["']?|docker/login-action|\b(?:PERSONAL_ACCESS_TOKEN|PAT_TOKEN|GHCR_TOKEN|CR_PAT)\b|\bdocker(?:\.exe)?\b[^\r\n]*(?:\bpush\b|--push\b)`)
	continuationPattern := regexp.MustCompile("[ \\t]*(?:`|\\\\)[ \\t]*\\r?\\n[ \\t]*")
	validatorPath := filepath.Clean(filepath.Join(repo, "scripts", "production-gates", "assert-pr-authority-maintenance.ps1"))
	var violations []string
	validatorSeen := false
	for _, root := range []string{filepath.Join(repo, ".github", "workflows"), filepath.Join(repo, "scripts")} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".yml" && ext != ".yaml" && ext != ".ps1" && ext != ".sh" && ext != ".js" && ext != ".cjs" {
				return nil
			}
			if filepath.Clean(path) == validatorPath {
				validatorSeen = true
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if filepath.Clean(path) != allowedWorkflow && filepath.Clean(path) != allowedScript && registryWriteSurface(path, validatorPath, content, writePattern, continuationPattern) {
				violations = append(violations, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if !validatorSeen {
		violations = append(violations, validatorPath)
	}
	return violations, nil
}

func registryWriteSurface(path, validatorPath string, content []byte, writePattern, continuationPattern *regexp.Regexp) bool {
	const inertSentinel = "    foreach ($forbidden in @('secrets.', 'packages: write', 'contents: write', 'id-token: write')) {\n"
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	if filepath.Clean(path) == validatorPath {
		if strings.Count(normalized, inertSentinel) != 1 {
			return true
		}
		normalized = strings.Replace(normalized, inertSentinel, "", 1)
	}
	return writePattern.MatchString(continuationPattern.ReplaceAllString(normalized, " "))
}

func testRepositorySingleWriterSentinel(t *testing.T, repo, allowedWorkflow, allowedScript string) {
	t.Helper()
	if violations, err := repositorySingleWriterViolations(repo, allowedWorkflow, allowedScript); err != nil {
		t.Fatal(err)
	} else if len(violations) != 0 {
		t.Fatalf("canonical authority validator is not the sole inert packages:write sentinel: %v", violations)
	}

	const inertSentinel = "    foreach ($forbidden in @('secrets.', 'packages: write', 'contents: write', 'id-token: write')) {\n"
	for _, test := range []struct {
		name          string
		validator     string
		omitValidator bool
		extraPath     string
		extraValue    string
		wantReject    bool
	}{
		{name: "canonical inert sentinel", validator: inertSentinel},
		{name: "canonical CRLF inert sentinel", validator: strings.ReplaceAll(inertSentinel, "\n", "\r\n")},
		{name: "missing validator", omitValidator: true, wantReject: true},
		{name: "missing sentinel", validator: "# read-only validator without the required permission guard\n", wantReject: true},
		{name: "second sentinel occurrence", validator: inertSentinel + inertSentinel, wantReject: true},
		{name: "changed sentinel context", validator: "    foreach ($forbidden in @('secrets.', \"packages: write\", 'contents: write', 'id-token: write')) {\n", wantReject: true},
		{name: "same-line payload after sentinel", validator: strings.TrimSuffix(inertSentinel, "\n") + "; docker image push ghcr.io/thebtf/engram\n", wantReject: true},
		{name: "registry command in validator", validator: inertSentinel + "docker push ghcr.io/thebtf/engram\n", wantReject: true},
		{name: "registry command with global option", validator: inertSentinel + "docker --context default push ghcr.io/thebtf/engram\n", wantReject: true},
		{name: "registry image subcommand", validator: inertSentinel + "docker image push ghcr.io/thebtf/engram\n", wantReject: true},
		{name: "registry compose subcommand", validator: inertSentinel + "docker compose push\n", wantReject: true},
		{name: "registry Windows executable", validator: inertSentinel + "docker.exe push ghcr.io/thebtf/engram\n", wantReject: true},
		{name: "registry PowerShell continuation", validator: inertSentinel + "docker `\n  --context default `\n  push ghcr.io/thebtf/engram\n", wantReject: true},
		{name: "registry shell continuation", validator: inertSentinel + "docker \\\n  image \\\n  push ghcr.io/thebtf/engram\n", wantReject: true},
		{name: "permission in another executable", validator: inertSentinel, extraPath: filepath.Join("scripts", "other.ps1"), extraValue: "packages: write\n", wantReject: true},
		{name: "permission with spaced key", validator: inertSentinel, extraPath: filepath.Join(".github", "workflows", "other.yml"), extraValue: "packages : write\n", wantReject: true},
		{name: "permission with quoted value", validator: inertSentinel, extraPath: filepath.Join(".github", "workflows", "other.yml"), extraValue: "packages: \"write\"\n", wantReject: true},
		{name: "non-write permission word", validator: inertSentinel, extraPath: filepath.Join(".github", "workflows", "other.yml"), extraValue: "packages: writer\n"},
		{name: "PAT token in another executable", validator: inertSentinel, extraPath: filepath.Join("scripts", "other.ps1"), extraValue: "PAT_TOKEN=secret\n", wantReject: true},
		{name: "GHCR token in another executable", validator: inertSentinel, extraPath: filepath.Join("scripts", "other.ps1"), extraValue: "GHCR_TOKEN=secret\n", wantReject: true},
		{name: "CR PAT in another executable", validator: inertSentinel, extraPath: filepath.Join("scripts", "other.ps1"), extraValue: "CR_PAT=secret\n", wantReject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := t.TempDir()
			validatorPath := filepath.Join(fixture, "scripts", "production-gates", "assert-pr-authority-maintenance.ps1")
			for _, path := range []string{filepath.Join(fixture, ".github", "workflows"), filepath.Dir(validatorPath)} {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if !test.omitValidator {
				if err := os.WriteFile(validatorPath, []byte(test.validator), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.extraPath != "" {
				path := filepath.Join(fixture, test.extraPath)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(test.extraValue), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			violations, err := repositorySingleWriterViolations(fixture, filepath.Join(fixture, "allowed.yml"), filepath.Join(fixture, "allowed.ps1"))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(violations) != 0; got != test.wantReject {
				t.Fatalf("rejected=%v, want %v; violations=%v", got, test.wantReject, violations)
			}
		})
	}
}

func inlineRunBodies(workflow string) []string {
	lines := strings.Split(strings.ReplaceAll(workflow, "\r\n", "\n"), "\n")
	var bodies []string
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "run:") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, "run:"))
		for next := index + 1; next < len(lines); next++ {
			nextLine := lines[next]
			if strings.TrimSpace(nextLine) == "" {
				body += "\n"
				continue
			}
			nextIndent := len(nextLine) - len(strings.TrimLeft(nextLine, " "))
			if nextIndent <= indent {
				break
			}
			body += "\n" + strings.TrimSpace(nextLine)
			index = next
		}
		bodies = append(bodies, body)
	}
	return bodies
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func verifyNuxtUICredentialFormAdvisoryUnreachable(t *testing.T, repo string) {
	t.Helper()
	operatorRoot := filepath.Join(repo, "apps", "operator-console")
	config := readFile(t, filepath.Join(operatorRoot, "nuxt.config.ts"))
	if !regexp.MustCompile(`(?m)^\s*ssr:\s*false\s*,?\s*(?://.*)?$`).MatchString(config) {
		t.Fatal("GHSA-gj2h-2fpw-fhv9 reachability changed: operator console is no longer explicitly SPA-only")
	}

	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(operatorRoot, "package-lock.json"))), &lock); err != nil {
		t.Fatalf("parse operator package lock: %v", err)
	}
	const reviewedVersion = "3.3.7"
	if got := lock.Packages["node_modules/@nuxt/ui"].Version; got != reviewedVersion {
		t.Fatalf("@nuxt/ui changed from reviewed version %s to %q; reclassify GHSA-gj2h-2fpw-fhv9 before release", reviewedVersion, got)
	}

	usage, err := findNuxtUICredentialFormUsage(operatorRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 0 {
		t.Fatalf("GHSA-gj2h-2fpw-fhv9 became reachable through UForm/UAuthForm source usage: %v", usage)
	}

	hostileRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostileRoot, "Login.vue"), []byte(`<template><UAuthForm /></template>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostileRoot, "LoginKebab.vue"), []byte(`<template><u-auth-form /></template>`), 0o600); err != nil {
		t.Fatal(err)
	}
	hostileUsage, err := findNuxtUICredentialFormUsage(hostileRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(hostileUsage) != 2 {
		t.Fatalf("Nuxt UI advisory guard did not reject PascalCase and kebab-case hostile UAuthForm fixtures: %v", hostileUsage)
	}
}

func findNuxtUICredentialFormUsage(root string) ([]string, error) {
	credentialForm := regexp.MustCompile(`(?i)(<\s*(?:u(?:auth)?form|u-(?:auth-)?form)\b|["'](?:u(?:auth)?form|u-(?:auth-)?form)["'])`)
	var matches []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".nuxt", ".output":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".vue", ".ts", ".js", ".mjs":
		default:
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if credentialForm.Match(content) {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			matches = append(matches, filepath.ToSlash(relative))
		}
		return nil
	})
	return matches, err
}

func verifyOperatorNativeOptionalTree(t *testing.T, repo string) {
	t.Helper()
	verifier := filepath.Join(repo, "apps", "operator-console", "scripts", "verify-native-optional-deps.mjs")
	command := exec.Command("node", verifier, "--self-test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native optional dependency verifier self-test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "native-optional-deps self-test=PASS") {
		t.Fatalf("native optional dependency verifier emitted no PASS proof: %s", output)
	}

	dockerfile := readFile(t, filepath.Join(repo, "Dockerfile"))
	for _, required := range []string{
		"npm ci --include=optional",
		"--fetch-retries=5",
		"--fetch-retry-factor=2",
		"--fetch-retry-mintimeout=1000",
		"--fetch-retry-maxtimeout=30000",
		"node scripts/verify-native-optional-deps.mjs",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("operator Docker build lacks native optional dependency contract %q", required)
		}
	}
}

func verifyOperatorComposeBuildVersion(t *testing.T, repo string) {
	t.Helper()
	compose := strings.ReplaceAll(readFile(t, filepath.Join(repo, "docker-compose.yml")), "\r\n", "\n")
	operatorStart := strings.Index(compose, "\n  operator-console:\n")
	secretsStart := strings.Index(compose, "\nsecrets:\n")
	if operatorStart < 0 || secretsStart <= operatorStart {
		t.Fatal("operator-console service block is missing from docker-compose.yml")
	}
	operatorBlock := compose[operatorStart:secretsStart]
	for _, required := range []string{
		"target: operator-console",
		"VERSION: ${ENGRAM_BUILD_VERSION:?set ENGRAM_BUILD_VERSION to vMAJOR.MINOR.PATCH[-prerelease] or sha-<full commit>}",
	} {
		if !strings.Contains(operatorBlock, required) {
			t.Fatalf("operator-console compose build lacks exact source version contract %q", required)
		}
	}
}

func testCanonicalReleaseValidation(t *testing.T, repo string) {
	t.Helper()
	fixture := writeRulesetFixture(t, exactRulesetFixture())
	sha := strings.Repeat("a", 40)
	for _, version := range []string{"v0.0.0", "v1.2.3", "v6.43.0-rc.1", "v1.2.3-alpha-1.2"} {
		version := version
		t.Run("accept "+version, func(t *testing.T) {
			runImageGate(t, repo, true,
				"-Mode", "ValidateRelease", "-ReleaseRef", "refs/tags/"+version,
				"-ExpectedSha", sha, "-ActualSha", sha, "-RulesetFixturePath", fixture)
		})
	}
	for _, version := range []string{
		"v1$(printf${IFS}INJECTED)", "v01.2.3", "v1.02.3", "v1.2.03",
		"v1.2.3+build", "v1.2.3-01", "v1.2", "v1.2.3 ", "v1.2.3;echo-INJECTED",
	} {
		version := version
		t.Run("reject "+fmt.Sprintf("%x", version), func(t *testing.T) {
			output := runImageGate(t, repo, false,
				"-Mode", "ValidateRelease", "-ReleaseRef", "refs/tags/"+version,
				"-ExpectedSha", sha, "-ActualSha", sha, "-RulesetFixturePath", fixture)
			if strings.Contains(output, "value=v1INJECTED") {
				t.Fatalf("hostile Git ref was evaluated as shell source: %s", output)
			}
		})
	}
}

func testWorkflowRunTrustMatrix(t *testing.T, repo string) {
	t.Helper()
	sha := strings.Repeat("a", 40)
	type mutation func(map[string]any)
	cases := []struct {
		name   string
		pass   bool
		mutate mutation
	}{
		{name: "exact protected release provenance", pass: true},
		{name: "additional non-authority status check is allowed", pass: true, mutate: func(f map[string]any) {
			checks := branchStatusChecks(f)
			branchStatusRule(f)["parameters"].(map[string]any)["required_status_checks"] = append(checks, map[string]any{"context": "tests", "integration_id": 15368})
		}},
		{name: "event action is not completed", mutate: func(f map[string]any) { f["event"].(map[string]any)["action"] = "requested" }},
		{name: "run failed", mutate: func(f map[string]any) {
			workflowRuns(f, func(run map[string]any) { run["conclusion"] = "failure" })
		}},
		{name: "manual dispatch spoof", mutate: func(f map[string]any) {
			workflowRuns(f, func(run map[string]any) { run["event"] = "workflow_dispatch" })
		}},
		{name: "fork head repository", mutate: func(f map[string]any) {
			workflowRuns(f, func(run map[string]any) { run["head_repository"] = map[string]any{"full_name": "attacker/engram"} })
		}},
		{name: "run id event api mismatch", mutate: func(f map[string]any) { f["api_run"].(map[string]any)["id"] = 999 }},
		{name: "workflow id event api mismatch", mutate: func(f map[string]any) { f["api_run"].(map[string]any)["workflow_id"] = 999 }},
		{name: "trusted workflow id spoof", mutate: func(f map[string]any) {
			workflowRuns(f, func(run map[string]any) { run["workflow_id"] = 999 })
		}},
		{name: "workflow path spoof", mutate: func(f map[string]any) {
			workflowRuns(f, func(run map[string]any) { run["path"] = ".github/workflows/docker-publish.yml" })
		}},
		{name: "inactive trusted workflow", mutate: func(f map[string]any) { f["trusted_workflow"].(map[string]any)["state"] = "disabled_manually" }},
		{name: "hostile shell-like tag", mutate: func(f map[string]any) {
			workflowRuns(f, func(run map[string]any) { run["head_branch"] = "v1$(printf${IFS}INJECTED)" })
		}},
		{name: "noncanonical tag", mutate: func(f map[string]any) {
			workflowRuns(f, func(run map[string]any) { run["head_branch"] = "v01.2.3" })
		}},
		{name: "event api sha mismatch", mutate: func(f map[string]any) { f["api_run"].(map[string]any)["head_sha"] = strings.Repeat("b", 40) }},
		{name: "tag peel mismatch", mutate: func(f map[string]any) { f["git"].(map[string]any)["tag_commit"] = strings.Repeat("b", 40) }},
		{name: "tag commit outside protected main", mutate: func(f map[string]any) { f["git"].(map[string]any)["main_ancestors"] = []string{} }},
		{name: "missing immutable tag ruleset", mutate: func(f map[string]any) { f["tag_rulesets"] = []any{} }},
		{name: "missing protected main ruleset", mutate: func(f map[string]any) { f["branch_rulesets"] = []any{} }},
		{name: "authority guard missing integration id", mutate: func(f map[string]any) { delete(branchStatusChecks(f)[0], "integration_id") }},
		{name: "authority guard string integration id", mutate: func(f map[string]any) { branchStatusChecks(f)[0]["integration_id"] = "15368" }},
		{name: "authority guard wrong integration id", mutate: func(f map[string]any) { branchStatusChecks(f)[0]["integration_id"] = 1 }},
		{name: "authority guard duplicate context", mutate: func(f map[string]any) {
			checks := branchStatusChecks(f)
			branchStatusRule(f)["parameters"].(map[string]any)["required_status_checks"] = append(checks, map[string]any{"context": "authority-guard", "integration_id": 15368})
		}},
		{name: "recovery bypass missing", mutate: func(f map[string]any) { branchRuleset(f)["bypass_actors"] = []any{} }},
		{name: "recovery bypass duplicated", mutate: func(f map[string]any) {
			actor := map[string]any{"actor_type": "User", "actor_id": 7106373, "bypass_mode": "pull_request"}
			branchRuleset(f)["bypass_actors"] = []any{actor, actor}
		}},
		{name: "recovery bypass always", mutate: func(f map[string]any) {
			branchRuleset(f)["bypass_actors"] = []any{map[string]any{"actor_type": "User", "actor_id": 7106373, "bypass_mode": "always"}}
		}},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixtureValue := workflowRunFixture(sha, "v6.43.0-rc.1")
			if test.mutate != nil {
				test.mutate(fixtureValue)
			}
			fixture := writeJSONFixture(t, fixtureValue)
			outputPath := filepath.Join(t.TempDir(), "validated.json")
			output := runImageGate(t, repo, test.pass,
				"-Mode", "ValidateWorkflowRun",
				"-WorkflowRunFixturePath", fixture,
				"-Repository", "thebtf/engram",
				"-OutputPath", outputPath)
			if test.pass {
				var result struct {
					Commit            string `json:"commit"`
					ArtifactsConsumed int    `json:"artifacts_consumed"`
				}
				data, err := os.ReadFile(outputPath)
				if err != nil || json.Unmarshal(data, &result) != nil {
					t.Fatalf("parse trusted workflow-run result: %v", err)
				}
				if result.Commit != sha || result.ArtifactsConsumed != 0 {
					t.Fatalf("validator consumed untrusted artifacts or changed the commit: %+v", result)
				}
			} else if strings.Contains(output, "value=v1INJECTED") {
				t.Fatalf("hostile workflow_run ref was evaluated as shell source: %s", output)
			}
		})
	}

	t.Run("trusted output rejects symlink or reparse escape", func(t *testing.T) {
		trustRoot := strings.TrimSpace(os.Getenv("RUNNER_TEMP"))
		if trustRoot == "" {
			trustRoot = t.TempDir()
		}
		fixtureRoot, err := os.MkdirTemp(trustRoot, "engram-trusted-output-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(fixtureRoot) })
		realTarget := filepath.Join(fixtureRoot, "real")
		if err := os.Mkdir(realTarget, 0o700); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(fixtureRoot, "escape")
		createDirectoryLink(t, realTarget, linkPath)
		fixture := writeJSONFixture(t, workflowRunFixture(sha, "v6.43.0-rc.1"))
		output := runImageGate(t, repo, false,
			"-Mode", "ValidateWorkflowRun",
			"-WorkflowRunFixturePath", fixture,
			"-Repository", "thebtf/engram",
			"-TrustedOutputRoot", trustRoot,
			"-OutputPath", filepath.Join(linkPath, "validated.json"))
		if !strings.Contains(strings.ToLower(output), "symlink/reparse") {
			t.Fatalf("trusted output rejection did not identify the link boundary: %s", output)
		}
	})
}

func workflowRunFixture(sha, version string) map[string]any {
	newRun := func() map[string]any {
		return map[string]any{
			"id": 123, "workflow_id": 456, "name": "Docker", "path": ".github/workflows/docker.yaml",
			"event": "push", "status": "completed", "conclusion": "success",
			"head_branch": version, "head_sha": sha,
			"head_repository": map[string]any{"full_name": "thebtf/engram"},
			"repository":      map[string]any{"full_name": "thebtf/engram"},
			"artifacts_url":   "https://attacker.invalid/untrusted-artifacts",
		}
	}
	return map[string]any{
		"event": map[string]any{
			"action":       "completed",
			"repository":   map[string]any{"full_name": "thebtf/engram"},
			"workflow_run": newRun(),
		},
		"api_run": newRun(),
		"trusted_workflow": map[string]any{
			"id": 456, "name": "Docker", "path": ".github/workflows/docker.yaml", "state": "active",
		},
		"repository":      map[string]any{"full_name": "thebtf/engram", "default_branch": "main"},
		"tag_rulesets":    exactRulesetFixture(),
		"branch_rulesets": exactBranchRulesetFixture(),
		"git":             map[string]any{"tag_commit": sha, "main_ancestors": []string{sha}},
	}
}

func workflowRuns(fixture map[string]any, mutate func(map[string]any)) {
	mutate(fixture["event"].(map[string]any)["workflow_run"].(map[string]any))
	mutate(fixture["api_run"].(map[string]any))
}

func branchRuleset(fixture map[string]any) map[string]any {
	return fixture["branch_rulesets"].([]map[string]any)[0]
}

func branchStatusRule(fixture map[string]any) map[string]any {
	return branchRuleset(fixture)["rules"].([]map[string]any)[2]
}

func branchStatusChecks(fixture map[string]any) []map[string]any {
	return branchStatusRule(fixture)["parameters"].(map[string]any)["required_status_checks"].([]map[string]any)
}

func createDirectoryLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Fatalf("create hostile symlink fixture: %v", err)
	}
	command := exec.Command("pwsh", "-NoProfile", "-Command", `New-Item -ItemType Junction -Path $args[0] -Target $args[1] | Out-Null`, link, target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create hostile junction fixture: %v\n%s", err, output)
	}
}

func testArtifactBridgeMatrix(t *testing.T, repo string) {
	t.Helper()
	const (
		artifactID   = 777
		workflowRun  = 12345
		artifactName = "engram-release-payload-12345-1"
	)
	digest := "sha256:" + strings.Repeat("d", 64)
	type metadataMutation func(map[string]any)
	metadataCases := []struct {
		name   string
		pass   bool
		mutate metadataMutation
	}{
		{name: "one exact same-run immutable artifact", pass: true},
		{name: "missing artifact", mutate: func(f map[string]any) { f["total_count"] = 0; f["artifacts"] = []any{} }},
		{name: "extra artifact", mutate: func(f map[string]any) {
			f["total_count"] = 2
			f["artifacts"] = append(f["artifacts"].([]map[string]any), map[string]any{"id": 778, "name": "extra", "digest": digest, "expired": false, "workflow_run": map[string]any{"id": workflowRun}})
		}},
		{name: "duplicate expected name", mutate: func(f map[string]any) {
			f["total_count"] = 2
			f["artifacts"] = append(f["artifacts"].([]map[string]any), map[string]any{"id": 778, "name": artifactName, "digest": digest, "expired": false, "workflow_run": map[string]any{"id": workflowRun}})
		}},
		{name: "artifact id mismatch", mutate: func(f map[string]any) { f["artifacts"].([]map[string]any)[0]["id"] = 778 }},
		{name: "artifact digest mismatch", mutate: func(f map[string]any) {
			f["artifacts"].([]map[string]any)[0]["digest"] = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "artifact expired", mutate: func(f map[string]any) { f["artifacts"].([]map[string]any)[0]["expired"] = true }},
		{name: "artifact from another workflow run", mutate: func(f map[string]any) {
			f["artifacts"].([]map[string]any)[0]["workflow_run"] = map[string]any{"id": 99999}
		}},
	}
	for _, test := range metadataCases {
		test := test
		t.Run("metadata "+test.name, func(t *testing.T) {
			fixtureValue := map[string]any{
				"total_count": 1,
				"artifacts": []map[string]any{{
					"id": artifactID, "name": artifactName, "digest": digest, "expired": false,
					"workflow_run": map[string]any{"id": workflowRun},
				}},
			}
			if test.mutate != nil {
				test.mutate(fixtureValue)
			}
			fixture := writeJSONFixture(t, fixtureValue)
			runImageGate(t, repo, test.pass,
				"-Mode", "ValidateArtifactMetadata",
				"-ArtifactListFixturePath", fixture,
				"-ExpectedArtifactID", strconv.Itoa(artifactID),
				"-ExpectedArtifactName", artifactName,
				"-ExpectedArtifactDigest", digest,
				"-CurrentRunID", strconv.Itoa(workflowRun),
				"-Repository", "thebtf/engram")
		})
	}

	sha := strings.Repeat("c", 40)
	payloadCases := []struct {
		name   string
		pass   bool
		mutate func(string)
	}{
		{name: "exact regular-file envelope", pass: true},
		{name: "extra file", mutate: func(root string) { _ = os.WriteFile(filepath.Join(root, "extra.txt"), []byte("extra"), 0o600) }},
		{name: "archive checksum mismatch", mutate: func(root string) { _ = os.WriteFile(filepath.Join(root, "server.tar"), []byte("changed"), 0o600) }},
		{name: "path traversal in bundle", mutate: func(root string) {
			bundle := readJSONMap(t, filepath.Join(root, "release-bundle.json"))
			bundle["images"].([]any)[0].(map[string]any)["archive"] = "../server.tar"
			writeJSONAt(t, filepath.Join(root, "release-bundle.json"), bundle)
		}},
		{name: "path traversal inside image archive", mutate: func(root string) {
			data := testTarArchiveEntry(t, "../escape", tar.TypeReg)
			path := filepath.Join(root, "server.tar")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			updatePayloadArchiveHash(t, root, 0, data)
		}},
		{name: "link inside outer image archive", mutate: func(root string) {
			data := testTarArchiveEntry(t, "linked", tar.TypeSymlink)
			path := filepath.Join(root, "server.tar")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			updatePayloadArchiveHash(t, root, 0, data)
		}},
		{name: "manifest commit mismatch", mutate: func(root string) {
			manifest := readJSONMap(t, filepath.Join(root, "final-image-set.json"))
			manifest["source_parent_commit"] = strings.Repeat("b", 40)
			writeJSONAt(t, filepath.Join(root, "final-image-set.json"), manifest)
		}},
		{name: "symlink entry", mutate: func(root string) {
			target := filepath.Join(root, "operator-console.tar")
			link := filepath.Join(root, "server.tar")
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil && runtime.GOOS == "windows" {
				command := exec.Command("pwsh", "-NoProfile", "-Command", `New-Item -ItemType SymbolicLink -Path $args[0] -Target $args[1] | Out-Null`, link, target)
				if output, commandErr := command.CombinedOutput(); commandErr != nil {
					t.Fatalf("create payload link fixture: %v / %v\n%s", err, commandErr, output)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range payloadCases {
		test := test
		t.Run("payload "+test.name, func(t *testing.T) {
			root := writeReleasePayloadFixture(t, sha, "v6.43.0-rc.1")
			if test.mutate != nil {
				test.mutate(root)
			}
			runImageGate(t, repo, test.pass,
				"-Mode", "ValidatePayload",
				"-PayloadRoot", root,
				"-ExpectedSha", sha,
				"-ReleaseVersion", "v6.43.0-rc.1")
		})
	}
	t.Run("payload immutable commit identity", func(t *testing.T) {
		version := "sha-" + sha
		root := writeReleasePayloadFixture(t, sha, version)
		runImageGate(t, repo, true,
			"-Mode", "ValidatePayload",
			"-PayloadRoot", root,
			"-ExpectedSha", sha,
			"-ReleaseVersion", version)
	})
}

func writeReleasePayloadFixture(t *testing.T, commit, version string) string {
	t.Helper()
	root := t.TempDir()
	ids := map[string]string{
		"server":           "sha256:" + strings.Repeat("1", 64),
		"operator_console": "sha256:" + strings.Repeat("2", 64),
		"postgres":         "sha256:" + strings.Repeat("3", 64),
	}
	manifestPath := filepath.Join(root, "final-image-set.json")
	writeJSONAt(t, manifestPath, map[string]any{
		"schema_version": 1, "status": "PASS", "source_parent_commit": commit,
		"build_version": version, "image_ids": ids,
	})
	images := make([]map[string]any, 0, 3)
	for _, image := range []struct {
		name    string
		archive string
		id      string
	}{
		{"server", "server.tar", ids["server"]},
		{"operator_console", "operator-console.tar", ids["operator_console"]},
		{"postgres", "postgres.tar", ids["postgres"]},
	} {
		data := testTarArchive(t, image.name)
		path := filepath.Join(root, image.archive)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		images = append(images, map[string]any{
			"name": image.name, "archive": image.archive, "image_id": image.id,
			"sha256": sha256Hex(data), "size_bytes": len(data),
		})
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeJSONAt(t, filepath.Join(root, "release-bundle.json"), map[string]any{
		"schema_version": 1, "source_commit": commit, "release_version": version,
		"manifest": map[string]any{"file": "final-image-set.json", "sha256": sha256Hex(manifestData), "size_bytes": len(manifestData)},
		"images":   images,
	})
	return root
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

func testTarArchive(t *testing.T, name string) []byte {
	t.Helper()
	return testTarArchiveEntry(t, "manifest.json", tar.TypeReg)
}

func testTarArchiveEntry(t *testing.T, entryName string, entryType byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	content := []byte("exact-image-archive")
	header := &tar.Header{Name: entryName, Mode: 0o600, Typeflag: entryType}
	if entryType == tar.TypeReg {
		header.Size = int64(len(content))
	} else {
		header.Linkname = "manifest.json"
	}
	if err := w.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if entryType == tar.TypeReg {
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func updatePayloadArchiveHash(t *testing.T, root string, imageIndex int, data []byte) {
	t.Helper()
	bundlePath := filepath.Join(root, "release-bundle.json")
	bundle := readJSONMap(t, bundlePath)
	image := bundle["images"].([]any)[imageIndex].(map[string]any)
	image["sha256"] = sha256Hex(data)
	image["size_bytes"] = len(data)
	writeJSONAt(t, bundlePath, bundle)
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeJSONAt(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testTagRulesetMatrix(t *testing.T, repo string) {
	t.Helper()
	sha := strings.Repeat("b", 40)
	positive := exactRulesetFixture()
	cases := []struct {
		name    string
		fixture any
		pass    bool
	}{
		{name: "exact active immutable namespace", fixture: positive, pass: true},
		{name: "missing", fixture: []any{}},
		{name: "duplicate", fixture: []any{positive[0], positive[0]}},
		{name: "disabled", fixture: mutateRuleset(positive, "enforcement", "disabled")},
		{name: "wrong target", fixture: mutateRuleset(positive, "target", "branch")},
		{name: "wrong include", fixture: mutateRuleset(positive, "include", []string{"refs/tags/release-*"})},
		{name: "exclusion", fixture: mutateRuleset(positive, "exclude", []string{"refs/tags/v6.43.0"})},
		{name: "bypass", fixture: mutateRuleset(positive, "bypass", []map[string]any{{"actor_id": 7106373, "actor_type": "RepositoryRole", "bypass_mode": "always"}})},
		{name: "missing deletion", fixture: mutateRuleset(positive, "rules", []map[string]any{{"type": "non_fast_forward"}})},
		{name: "missing non fast forward", fixture: mutateRuleset(positive, "rules", []map[string]any{{"type": "deletion"}})},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := writeRulesetFixture(t, test.fixture)
			runImageGate(t, repo, test.pass,
				"-Mode", "ValidateRelease", "-ReleaseRef", "refs/tags/v6.43.0-rc.1",
				"-ExpectedSha", sha, "-ActualSha", sha, "-RulesetFixturePath", fixture)
		})
	}
}

func testRegistryCASMatrix(t *testing.T, repo string) {
	t.Helper()
	commit := strings.Repeat("c", 40)
	ids := map[string]string{
		"server":           "sha256:" + strings.Repeat("1", 64),
		"operator_console": "sha256:" + strings.Repeat("2", 64),
		"postgres":         "sha256:" + strings.Repeat("3", 64),
	}
	manifest := writeJSONFixture(t, map[string]any{"source_parent_commit": commit, "image_ids": ids})
	version := "v6.43.0-rc.1"
	expectedRefs := expectedPublicationRefs(version, commit)

	t.Run("all destinations absent", func(t *testing.T) {
		registry := writeJSONFixture(t, map[string]any{"refs": map[string]any{}})
		outputPath := filepath.Join(t.TempDir(), "plan.json")
		runImageGate(t, repo, true,
			"-Mode", "PlanPublication", "-ManifestPath", manifest, "-ReleaseVersion", version,
			"-RegistryFixturePath", registry, "-OutputPath", outputPath)
		assertPublicationPlan(t, outputPath, expectedRefs, 6)
	})

	t.Run("idempotent exact protected tag", func(t *testing.T) {
		refs := make(map[string]any)
		for ref, id := range expectedRefs {
			refs[ref] = map[string]any{"config_digest": id, "manifest_digest": "sha256:" + strings.Repeat("d", 64)}
		}
		registry := writeJSONFixture(t, map[string]any{"refs": refs})
		outputPath := filepath.Join(t.TempDir(), "plan.json")
		runImageGate(t, repo, true,
			"-Mode", "PlanPublication", "-ManifestPath", manifest, "-ReleaseVersion", version,
			"-RegistryFixturePath", registry, "-OutputPath", outputPath)
		assertPublicationPlan(t, outputPath, expectedRefs, 0)
	})

	for _, name := range []string{"same release different image", "existing registry mismatch"} {
		name := name
		t.Run(name, func(t *testing.T) {
			refs := make(map[string]any)
			for ref, id := range expectedRefs {
				refs[ref] = map[string]any{"config_digest": id, "manifest_digest": "sha256:" + strings.Repeat("e", 64)}
			}
			for ref := range refs {
				refs[ref] = map[string]any{"config_digest": "sha256:" + strings.Repeat("f", 64), "manifest_digest": "sha256:" + strings.Repeat("e", 64)}
				break
			}
			registry := writeJSONFixture(t, map[string]any{"refs": refs})
			runImageGate(t, repo, false,
				"-Mode", "PlanPublication", "-ManifestPath", manifest, "-ReleaseVersion", version,
				"-RegistryFixturePath", registry, "-OutputPath", filepath.Join(t.TempDir(), "plan.json"))
		})
	}
}

func testImmutableTagMovement(t *testing.T, repo string) {
	t.Helper()
	fixture := writeRulesetFixture(t, exactRulesetFixture())
	initial := strings.Repeat("1", 40)
	advanced := strings.Repeat("2", 40)
	runImageGate(t, repo, true,
		"-Mode", "ValidateRelease", "-ReleaseRef", "refs/tags/v1.0.0",
		"-ExpectedSha", initial, "-ActualSha", initial, "-RulesetFixturePath", fixture)
	runImageGate(t, repo, false,
		"-Mode", "ValidateRelease", "-ReleaseRef", "refs/tags/v1.0.0",
		"-ExpectedSha", initial, "-ActualSha", advanced, "-RulesetFixturePath", fixture)
	unprotected := writeRulesetFixture(t, []any{})
	runImageGate(t, repo, false,
		"-Mode", "ValidateRelease", "-ReleaseRef", "refs/tags/v1.0.0",
		"-ExpectedSha", initial, "-ActualSha", initial, "-RulesetFixturePath", unprotected)
}

func runImageGate(t *testing.T, repo string, expectSuccess bool, args ...string) string {
	t.Helper()
	script := filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1")
	commandArgs := append([]string{"-NoProfile", "-File", script}, args...)
	output, err := exec.Command("pwsh", commandArgs...).CombinedOutput()
	if expectSuccess && err != nil {
		t.Fatalf("image gate unexpectedly failed: %v\n%s", err, output)
	}
	if !expectSuccess && err == nil {
		t.Fatalf("image gate unexpectedly accepted adversarial fixture:\n%s", output)
	}
	return string(output)
}

func exactRulesetFixture() []map[string]any {
	return []map[string]any{{
		"id": 9001, "name": "immutable v releases", "target": "tag", "enforcement": "active",
		"conditions":    map[string]any{"ref_name": map[string]any{"include": []string{"refs/tags/v*"}, "exclude": []string{}}},
		"rules":         []map[string]any{{"type": "deletion"}, {"type": "non_fast_forward"}},
		"bypass_actors": []any{},
	}}
}

func exactBranchRulesetFixture() []map[string]any {
	return []map[string]any{{
		"id": 9100, "name": "protected main authority", "target": "branch", "enforcement": "active",
		"conditions": map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main"}, "exclude": []string{}}},
		"rules": []map[string]any{
			{"type": "deletion"},
			{"type": "non_fast_forward"},
			{
				"type": "required_status_checks",
				"parameters": map[string]any{
					"strict_required_status_checks_policy": true,
					"required_status_checks":               []map[string]any{{"context": "authority-guard", "integration_id": 15368}},
				},
			},
		},
		"bypass_actors": []map[string]any{{"actor_type": "User", "actor_id": 7106373, "bypass_mode": "pull_request"}},
	}}
}

func mutateRuleset(source []map[string]any, field string, value any) []map[string]any {
	raw, _ := json.Marshal(source)
	var clone []map[string]any
	_ = json.Unmarshal(raw, &clone)
	rule := clone[0]
	switch field {
	case "include", "exclude":
		conditions := rule["conditions"].(map[string]any)
		refName := conditions["ref_name"].(map[string]any)
		refName[field] = value
	case "bypass":
		rule["bypass_actors"] = value
	default:
		rule[field] = value
	}
	return clone
}

func writeRulesetFixture(t *testing.T, value any) string {
	t.Helper()
	return writeJSONFixture(t, value)
}

func writeJSONFixture(t *testing.T, value any) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func expectedPublicationRefs(version, commit string) map[string]string {
	ids := []struct {
		repository string
		id         string
	}{
		{"ghcr.io/thebtf/engram", "sha256:" + strings.Repeat("1", 64)},
		{"ghcr.io/thebtf/engram-operator-console", "sha256:" + strings.Repeat("2", 64)},
		{"ghcr.io/thebtf/engram-postgres", "sha256:" + strings.Repeat("3", 64)},
	}
	refs := make(map[string]string, 6)
	for _, image := range ids {
		refs[image.repository+":"+version] = image.id
		refs[image.repository+":sha-"+commit] = image.id
	}
	return refs
}

func assertPublicationPlan(t *testing.T, path string, expected map[string]string, expectedPushes int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Destinations []struct {
			Reference    string `json:"reference"`
			ConfigDigest string `json:"config_digest"`
			Action       string `json:"action"`
		} `json:"destinations"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Destinations) != len(expected) {
		t.Fatalf("publication plan has %d destinations, want %d", len(plan.Destinations), len(expected))
	}
	pushes := 0
	seen := make(map[string]bool, len(plan.Destinations))
	for _, destination := range plan.Destinations {
		want, ok := expected[destination.Reference]
		if !ok {
			t.Fatalf("publication plan leaked non-canonical alias %q", destination.Reference)
		}
		if destination.ConfigDigest != want {
			t.Fatalf("publication plan changed exact image identity for %s", destination.Reference)
		}
		if destination.Action == "push" {
			pushes++
		}
		seen[destination.Reference] = true
	}
	if len(seen) != len(expected) || pushes != expectedPushes {
		t.Fatalf("publication plan coverage mismatch: refs=%d pushes=%d, want refs=%d pushes=%d", len(seen), pushes, len(expected), expectedPushes)
	}
}

type gitRefFreshnessFixture struct {
	t           *testing.T
	work        string
	consumer    string
	initialSHA  string
	advancedSHA string
}

func newGitRefFreshnessFixture(t *testing.T) *gitRefFreshnessFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "source")
	consumer := filepath.Join(root, "consumer")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, "", "init", "-b", "main", work)
	runGit(t, work, "config", "user.name", "Engram Image Gate")
	runGit(t, work, "config", "user.email", "image-gate@example.invalid")
	writeGitFixtureFile(t, work, "initial")
	runGit(t, work, "add", "fixture.txt")
	runGit(t, work, "commit", "-m", "initial")
	initialSHA := runGit(t, work, "rev-parse", "HEAD")
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "tag", "-a", "v1.0.0", "-m", "release", initialSHA)
	runGit(t, work, "tag", "v1.0.1", initialSHA)
	runGit(t, work, "push", "origin", "main", "refs/tags/v1.0.0", "refs/tags/v1.0.1")
	writeGitFixtureFile(t, work, "advanced")
	runGit(t, work, "add", "fixture.txt")
	runGit(t, work, "commit", "-m", "advanced")
	advancedSHA := runGit(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "main")
	runGit(t, "", "init", consumer)
	runGit(t, consumer, "remote", "add", "origin", remote)
	return &gitRefFreshnessFixture{
		t: t, work: work, consumer: consumer,
		initialSHA: initialSHA, advancedSHA: advancedSHA,
	}
}

func (fixture *gitRefFreshnessFixture) requireMatch(sourceRef, expectedSHA string) error {
	guardRef := "refs/engram-publish-guard/test"
	runGit(fixture.t, fixture.consumer, "update-ref", "-d", guardRef)
	runGit(fixture.t, fixture.consumer, "fetch", "--no-tags", "--force", "origin", "+"+sourceRef+":"+guardRef)
	actualSHA := runGit(fixture.t, fixture.consumer, "rev-parse", "--verify", guardRef+"^{commit}")
	if actualSHA != expectedSHA {
		return fmt.Errorf("live ref %s resolved to %s, expected %s", sourceRef, actualSHA, expectedSHA)
	}
	return nil
}

func writeGitFixtureFile(t *testing.T, work, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, "fixture.txt"), []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, work string, args ...string) string {
	t.Helper()
	if work != "" {
		args = append([]string{"-C", work}, args...)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func runOperatorFixture(t *testing.T, name, network string, extra ...string) {
	t.Helper()
	args := []string{
		"run", "-d", "--name", name,
		"--network", network,
		"--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,uid=65532,gid=65532,mode=0700,size=64m",
		"--health-start-period", "1ms", "--health-interval", "1s", "--health-timeout", "4s", "--health-retries", "2",
	}
	args = append(args, extra...)
	args = append(args, imageFromEnv("ENGRAM_OPERATOR_IMAGE", defaultOperatorImage))
	runDocker(t, nil, args...)
}

func startFakeNodeBackend(t *testing.T, name, network, response string) {
	t.Helper()
	script := "const http=require('http');http.createServer((req,res)=>{" + response + "}).listen(37777,'0.0.0.0')"
	runDocker(t, nil,
		"run", "-d", "--name", name,
		"--network", network, "--network-alias", name,
		"--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--entrypoint", "/nodejs/bin/node",
		imageFromEnv("ENGRAM_OPERATOR_IMAGE", defaultOperatorImage),
		"-e", script,
	)
}

func startServerStack(t *testing.T) stackFixture {
	t.Helper()
	prefix := uniqueResource("engram-prc-image-test")
	fixture := stackFixture{
		prefix:         prefix,
		network:        prefix + "-net",
		postgresVolume: prefix + "-pgdata",
		serverVolume:   prefix + "-server-home",
		postgres:       prefix + "-postgres",
		server:         prefix + "-server",
	}
	t.Cleanup(func() {
		removeContainer(fixture.server)
		removeContainer(fixture.postgres)
		removeVolume(fixture.serverVolume)
		removeVolume(fixture.postgresVolume)
		removeNetwork(fixture.network)
	})
	runDocker(t, nil, "network", "create", fixture.network)
	runDocker(t, nil, "volume", "create", fixture.postgresVolume)
	runDocker(t, nil, "volume", "create", fixture.serverVolume)
	startPostgresContainer(t, fixture.postgres, fixture.network, fixture.postgresVolume)
	waitHealthy(t, fixture.postgres, 90*time.Second)

	serverImage := imageFromEnv("ENGRAM_SERVER_IMAGE", defaultServerImage)
	runDocker(t, nil,
		"run", "-d", "--name", fixture.server,
		"--network", fixture.network, "--network-alias", "server",
		"-p", "127.0.0.1::37777",
		"--user", "65532:65532", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,uid=65532,gid=65532,mode=0700,size=64m",
		"-v", fixture.serverVolume+":/var/lib/engram",
		"-e", "HOME=/var/lib/engram",
		"-e", "DATABASE_DSN=postgres://engram:engram@postgres:5432/engram?sslmode=disable",
		"-e", "ENGRAM_AUTH_DISABLED=true",
		serverImage,
	)
	waitHealthy(t, fixture.server, 120*time.Second)
	return fixture
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

func imageFromEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func inspectImage(t *testing.T, image string) imageInspect {
	t.Helper()
	out := runDocker(t, nil, "image", "inspect", image)
	var records []imageInspect
	if err := json.Unmarshal(out, &records); err != nil || len(records) != 1 {
		t.Fatalf("parse docker image inspect for %s: %v", image, err)
	}
	return records[0]
}

func requireEnv(t *testing.T, env []string, key, expected string) {
	t.Helper()
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if strings.TrimPrefix(entry, prefix) != expected {
				t.Fatalf("%s mismatch: %q", key, entry)
			}
			return
		}
	}
	t.Fatalf("%s is absent from image environment", key)
}

func requireHealthCommand(t *testing.T, health *dockerHealthConfig, parts ...string) {
	t.Helper()
	if health == nil {
		t.Fatal("image has no Docker HEALTHCHECK")
	}
	joined := strings.Join(health.Test, " ")
	for _, part := range parts {
		if !strings.Contains(joined, part) {
			t.Fatalf("healthcheck %q does not contain %q", joined, part)
		}
	}
}

func requireProvenanceLabels(t *testing.T, labels map[string]string) {
	t.Helper()
	if labels["org.opencontainers.image.source"] != "https://github.com/thebtf/engram" {
		t.Fatalf("image source label mismatch: %q", labels["org.opencontainers.image.source"])
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{40}$`, labels["org.opencontainers.image.revision"]); !matched {
		t.Fatalf("image revision label is not a full commit SHA: %q", labels["org.opencontainers.image.revision"])
	}
	if strings.TrimSpace(labels["org.opencontainers.image.version"]) == "" {
		t.Fatal("image version label is missing")
	}
}

func requireFileContains(t *testing.T, path string, fragments ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if !bytes.Contains(content, []byte(fragment)) {
			t.Fatalf("%s does not contain required contract %q", path, fragment)
		}
	}
}

func requireFileNotContains(t *testing.T, path string, fragments ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if bytes.Contains(content, []byte(fragment)) {
			t.Fatalf("%s retains forbidden contract %q", path, fragment)
		}
	}
}

func runDocker(t *testing.T, input []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("docker", args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return bytes.TrimSpace(out)
}

func dockerBestEffort(args ...string) {
	cmd := exec.Command("docker", args...)
	_, _ = cmd.CombinedOutput()
}

func waitHealthy(t *testing.T, container string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", container).CombinedOutput()
		status := strings.TrimSpace(string(out))
		if status == "healthy" {
			return
		}
		if status == "unhealthy" {
			logs, _ := exec.Command("docker", "logs", "--tail", "40", container).CombinedOutput()
			t.Fatalf("container %s became unhealthy:\n%s", container, logs)
		}
		time.Sleep(time.Second)
	}
	logs, _ := exec.Command("docker", "logs", "--tail", "40", container).CombinedOutput()
	t.Fatalf("container %s did not become healthy:\n%s", container, logs)
}

func waitNotHealthy(t *testing.T, container string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", container).CombinedOutput()
		status := strings.TrimSpace(string(out))
		if strings.Contains(status, "unhealthy") || strings.HasPrefix(status, "exited ") || strings.HasPrefix(status, "dead ") {
			return
		}
		if strings.Contains(status, "healthy") {
			t.Fatalf("negative fixture %s became healthy", container)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("negative fixture %s did not reach a terminal non-healthy state", container)
}

func requireHardenedContainer(t *testing.T, name, user string) {
	t.Helper()
	out := runDocker(t, nil, "inspect", name)
	var records []containerInspect
	if err := json.Unmarshal(out, &records); err != nil || len(records) != 1 {
		t.Fatalf("parse container inspect: %v", err)
	}
	record := records[0]
	if record.Config.User != user && record.Config.User != strings.Split(user, ":")[0] {
		t.Fatalf("container user mismatch: %q", record.Config.User)
	}
	if !record.HostConfig.ReadonlyRootfs {
		t.Fatal("container root filesystem is writable")
	}
	if !containsFold(record.HostConfig.CapDrop, "ALL") {
		t.Fatalf("container does not drop ALL capabilities: %v", record.HostConfig.CapDrop)
	}
	if !containsSubstring(record.HostConfig.SecurityOpt, "no-new-privileges") {
		t.Fatalf("container lacks no-new-privileges: %v", record.HostConfig.SecurityOpt)
	}
}

func requireVolumeMetadata(t *testing.T, volume, inspectionImage, expected string) {
	t.Helper()
	out := runDocker(t, nil,
		"run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh",
		"-v", volume+":/data:ro", inspectionImage,
		"-c", "stat -c '%u:%g:%a' /data",
	)
	if got := strings.TrimSpace(string(out)); got != expected {
		t.Fatalf("volume %s metadata=%q want=%q", volume, got, expected)
	}
}

func requireVolumePathMetadata(t *testing.T, volume, inspectionImage, path, expected string) {
	t.Helper()
	out := runDocker(t, nil,
		"run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh",
		"-v", volume+":/data:ro", inspectionImage,
		"-c", "stat -c '%u:%g:%a' "+path,
	)
	if got := strings.TrimSpace(string(out)); got != expected {
		t.Fatalf("volume path %s metadata=%q want=%q", path, got, expected)
	}
}

func readVolumeFile(t *testing.T, volume, inspectionImage, path string) []byte {
	t.Helper()
	return runDocker(t, nil,
		"run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh",
		"-v", volume+":/data:ro", inspectionImage,
		"-c", "cat "+path,
	)
}

func mappedURL(t *testing.T, container, port string) string {
	t.Helper()
	out := runDocker(t, nil, "port", container, port)
	line := strings.Split(string(out), "\n")[0]
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		t.Fatalf("unexpected docker port output %q", line)
	}
	value, err := strconv.Atoi(strings.TrimSpace(line[idx+1:]))
	if err != nil {
		t.Fatalf("unexpected mapped port %q: %v", line, err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", value)
}

func requireHTTP(t *testing.T, url string, status int) []byte {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("GET %s status=%d want=%d body=%q", url, response.StatusCode, status, body)
	}
	return body
}

func requireHTTPEventually(t *testing.T, url string, status int, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastError error
	for time.Now().Before(deadline) {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if readErr == nil && response.StatusCode == status {
				return body
			}
			lastError = fmt.Errorf("status=%d read_error=%v body=%q", response.StatusCode, readErr, body)
		} else {
			lastError = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("GET %s did not reach status %d: %v", url, status, lastError)
	return nil
}

func requireReferencedAsset(t *testing.T, baseURL string, root []byte) {
	t.Helper()
	assetPattern := regexp.MustCompile(`(?:src|href)="([^"?]*_nuxt/[^"?]+\.(?:js|css))`)
	match := assetPattern.FindSubmatch(root)
	if len(match) != 2 {
		t.Fatal("operator root contains no generated Nuxt JS/CSS asset")
	}
	path := string(match[1])
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	requireHTTP(t, baseURL+path, http.StatusOK)
}

func uniqueResource(prefix string) string {
	if gatePrefix := strings.TrimSpace(os.Getenv("ENGRAM_TEST_RESOURCE_PREFIX")); gatePrefix != "" {
		prefix = gatePrefix + "-" + prefix
	}
	return fmt.Sprintf("%s-%d-%d", prefix, os.Getpid(), time.Now().UnixNano())
}

func removeContainer(name string) { dockerBestEffort("rm", "-f", name) }
func removeVolume(name string)    { dockerBestEffort("volume", "rm", name) }
func removeNetwork(name string)   { dockerBestEffort("network", "rm", name) }

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, expected string) bool {
	for _, value := range values {
		if strings.Contains(value, expected) {
			return true
		}
	}
	return false
}
