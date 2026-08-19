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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
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
		"ENGRAM_LLM_URL: \"${ENGRAM_LLM_URL:-}\"",
		"ENGRAM_LLM_MODEL: \"${ENGRAM_LLM_MODEL:-chat-default}\"",
		"ENGRAM_LLM_API_KEY: \"${ENGRAM_LLM_API_KEY:-}\"",
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
			"ENGRAM_LLM_URL: \"${ENGRAM_LLM_URL:-}\"",
			"ENGRAM_LLM_MODEL: \"${ENGRAM_LLM_MODEL:-chat-default}\"",
			"ENGRAM_LLM_API_KEY: \"${ENGRAM_LLM_API_KEY:-}\"",
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
	requireFileContains(t, filepath.Join(repo, "docker-compose.yml"),
		"target: operator-console",
		"VERSION: ${ENGRAM_BUILD_VERSION:?set ENGRAM_BUILD_VERSION",
	)
	requireFileContains(t, filepath.Join(repo, "README.md"),
		"ENGRAM_SERVER_IMAGE=engram-local-server",
		"ENGRAM_OPERATOR_IMAGE=engram-local-operator-console",
		"ENGRAM_POSTGRES_IMAGE=engram-local-postgres",
		"ENGRAM_BUILD_VERSION=sha-$commit",
	)
	requireFileContains(t, filepath.Join(repo, "docs", "DEPLOYMENT.md"),
		"--cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER",
		"POSTGRES_MIGRATION_IMAGE='ghcr.io/thebtf/engram-postgres@sha256:<postgres-manifest-digest>'",
		"chown -R 70:70 /var/lib/postgresql/data && chmod 0700 /var/lib/postgresql/data",
		"The `stat` command must print `70:70:700`",
	)
	verificationPath := filepath.Join(repo, ".github", "workflows", "docker.yaml")
	publisherPath := filepath.Join(repo, ".github", "workflows", "docker-publish.yml")
	latestPromotionPath := filepath.Join(repo, ".github", "workflows", "promote-latest-release-images.yml")
	for _, workflowPath := range []string{verificationPath, publisherPath, latestPromotionPath} {
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

	verification := readFile(t, verificationPath)
	for _, fragment := range []string{
		"branches: [main]",
		"tags: [\"v*\"]",
		"workflow_dispatch:",
		"verify-images:",
		"Install Trivy",
		"trivy_0.72.0_Linux-64bit.tar.gz",
		"bbb64b9695866ce4a7a8f5c9592002c5961cab378577fa3f8a040df362b9b2ea",
		"$GITHUB_PATH",
		"trivy_bin/trivy\" --version",
		"Mode BuildAndScan",
		"include-hidden-files: true",
	} {
		if !strings.Contains(verification, fragment) {
			t.Fatalf("unprivileged Docker workflow lacks verification contract %q", fragment)
		}
	}
	trivyInstallIndex := strings.Index(verification, "Install Trivy")
	buildAndScanIndex := strings.Index(verification, "Build, scan, and exercise exact images without registry authority")
	if trivyInstallIndex < 0 || buildAndScanIndex < 0 || trivyInstallIndex > buildAndScanIndex {
		t.Fatal("Trivy must be installed before image verification")
	}

	for _, forbidden := range []string{"packages: write", "docker/login-action@", "Mode Publish", "Mode ValidateWorkflowRun"} {
		if strings.Contains(verification, forbidden) {
			t.Fatalf("unprivileged Docker workflow acquired publication authority %q", forbidden)
		}
	}
	if !strings.Contains(verification, "  schedule:\n") || !strings.Contains(verification, "cron: \"17 3 * * *\"") {
		t.Fatal("Docker workflow must schedule the daily UTC image freshness checks")
	}
	rescanIndex := strings.Index(verification, "  rescan-published-images:")
	if rescanIndex < 0 {
		t.Fatal("Docker workflow lacks the scheduled published-image rescan job")
	}
	publishedRescan := verification[rescanIndex:]
	for _, required := range []string{
		"if: github.event_name == 'schedule'", "contents: read",
		"Resolve latest published release through read-only GitHub API", "/releases/latest", "/git/ref/tags/$version", "READ_ONLY_GITHUB_TOKEN",
		"-TimeoutSec 30", "-MaximumRetryCount 3", "-RetryIntervalSec 2", "source_commit", "EXPECTED_SOURCE_COMMIT",
		"Rescan published immutable images without registry authority", "Mode ScanPublished", "-ExpectedSha $env:EXPECTED_SOURCE_COMMIT", "-NoAllowlist",
		"-ArtifactRoot (Join-Path $env:EVIDENCE_ROOT 'scan')", "-TrustedOutputRoot $env:RUNNER_TEMP",
		"Upload scheduled published-image rescan evidence", "if: always()",
	} {
		if !strings.Contains(publishedRescan, required) {
			t.Fatalf("scheduled published-image rescan lacks contract %q", required)
		}
	}
	for _, forbidden := range []string{"needs:", "packages: write", "docker/login-action@", "Mode Publish", "docker pull", "docker push"} {
		if strings.Contains(publishedRescan, forbidden) {
			t.Fatalf("scheduled published-image rescan exceeds read-only authority with %q", forbidden)
		}
	}

	scanner := readFile(t, filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1"))
	for _, fragment := range []string{
		"toolVersions.trivy",
		"Invoke-LoggedNative -File 'trivy'",
		"'image', '--image-src', 'docker'",
		"'--scanners', 'vuln'",
		"'--severity', 'HIGH,CRITICAL'",
		"trivy.sarif",
		"type=docker,rewrite-timestamp=true,unpack=false",
		"Resolve-LocalImageId",
		"ExpectedConfigDigest",
		"config.digest",
		"image_ids = $imageIds",
		"local_runtime_image_ids = $localImageIds",
		"ManifestName = 'operator_console'",
		"$scanCounts[$target.ManifestName]",
	} {
		if !strings.Contains(scanner, fragment) {
			t.Fatalf("image gate lacks unprivileged scanner contract %q", fragment)
		}
	}
	goBuilder := "golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36"
	requireFileContains(t, filepath.Join(repo, "Dockerfile"), goBuilder)
	if !strings.Contains(scanner, "go_builder = '"+goBuilder+"'") {
		t.Fatal("image acceptance evidence records a Go builder that differs from the Dockerfile source")
	}
	if count := strings.Count(scanner, "'type=docker,rewrite-timestamp=true,unpack=false'"); count != 3 {
		t.Fatalf("image gate must normalize timestamps for exactly three Docker image builds, got %d", count)
	}
	for _, forbidden := range []string{
		"toolVersions.scout", "'scout', 'cves'", "docker-scout",
		"--load", "'type=docker,rewrite-timestamp=true'",
	} {
		if strings.Contains(scanner, forbidden) {
			t.Fatalf("image gate retains forbidden scanner or exporter contract %q", forbidden)
		}
	}
	publishedModeStart := strings.Index(scanner, "function Invoke-ScanPublished")
	publishedModeEnd := strings.Index(scanner, "switch ($Mode)")
	if publishedModeStart < 0 || publishedModeEnd <= publishedModeStart {
		t.Fatal("image gate lacks the published-image scan mode")
	}
	publishedMode := scanner[publishedModeStart:publishedModeEnd]
	for _, required := range []string{
		"Assert-CanonicalPublishedReleaseVersion", "Assert-FullCommitSha", "Get-LiveRemoteIdentity", "'image', '--download-db-only'",
		"'image', '--skip-db-update'", "tag_reference", "commit_reference", "manifest_digest", "commit_manifest_digest", "scan_reference",
		"source_commit", "sha-$sourceCommit", "published-image-scan-summary.json", "trivy-version.log", "version_log_sha256", "-AllowFailure", "-NoAllowlist",
	} {
		if !strings.Contains(publishedMode, required) {
			t.Fatalf("published-image scan mode lacks contract %q", required)
		}
	}
	for _, forbidden := range []string{"docker login", "docker pull", "docker push", "docker run", "--image-src', 'docker'"} {
		if strings.Contains(publishedMode, forbidden) {
			t.Fatalf("published-image scan mode violates read-only remote scan authority with %q", forbidden)
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
		"Install Trivy",
		"trivy_0.72.0_Linux-64bit.tar.gz",
		"bbb64b9695866ce4a7a8f5c9592002c5961cab378577fa3f8a040df362b9b2ea",
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
		"RULESET_AUDIT_TOKEN: ${{ secrets.MARKETPLACE_PAT }}",
		`RULESET_AUDIT_TOKEN: ""`,
		"server.trivy.sarif",
		"Validate the exact trusted evidence envelope after credential erasure\n        if: always() && steps.publish.outcome != 'skipped'",
		"Upload trusted publication evidence after credential erasure\n        if: always() && steps.publish.outcome != 'skipped'",
		"id: publish",
		"-CredentialDirectoryPath $env:DOCKER_CONFIG",
	} {
		if !strings.Contains(publisher, fragment) {
			t.Fatalf("trusted workflow_run publisher lacks contract %q", fragment)
		}
	}
	publisherTrivyInstallIndex := strings.Index(publisher, "Install Trivy")
	publisherBuildAndScanIndex := strings.Index(publisher, "Build, scan, exercise, and export exact image data without package authority")
	if publisherTrivyInstallIndex < 0 || publisherBuildAndScanIndex < 0 || publisherTrivyInstallIndex > publisherBuildAndScanIndex {
		t.Fatal("Trivy must be installed before release image preparation")
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
	validators := []struct {
		name    string
		section string
	}{
		{
			name: "prepare-release",
			section: workflowStepSection(t, publisher,
				"Independently validate the triggering run, tag, and protected-main provenance",
				"Checkout exact validated candidate without workflow credentials"),
		},
		{
			name: "publish-images",
			section: workflowStepSection(t, publisher,
				"Revalidate workflow, tag, rulesets, and protected-main provenance on the fresh runner",
				"Confirm preparation and fresh-runner provenance agree"),
		},
	}
	for _, validator := range validators {
		if got := strings.Count(validator.section, "RULESET_AUDIT_TOKEN: ${{ secrets.MARKETPLACE_PAT }}"); got != 1 {
			t.Fatalf("%s provenance validator must receive exactly one bypass-aware ruleset credential binding, got %d", validator.name, got)
		}
		if got := strings.Count(validator.section, "-GitHubToken $env:RULESET_AUDIT_TOKEN"); got != 1 {
			t.Fatalf("%s provenance validator must consume exactly one ruleset audit credential, got %d", validator.name, got)
		}
	}
	if got := strings.Count(publisher, `RULESET_AUDIT_TOKEN: ""`); got != 1 {
		t.Fatalf("candidate execution must explicitly clear the ruleset audit credential, got %d clearings", got)
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
	t.Run("published immutable image scan evidence and failures", func(t *testing.T) {
		testPublishedImageScanGate(t, repo)
	})

	t.Run("repository controlled release and latest writers", func(t *testing.T) {
		testRepositoryReleaseAndLatestWriters(t, repo)
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
	t.Run("live ruleset API array response", func(t *testing.T) {
		testLiveRulesetAPIArray(t, repo)
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

func testRepositoryReleaseAndLatestWriters(t *testing.T, repo string) {
	t.Helper()
	releaseWorkflowPath := filepath.Clean(filepath.Join(repo, ".github", "workflows", "docker-publish.yml"))
	latestWorkflowPath := filepath.Clean(filepath.Join(repo, ".github", "workflows", "promote-latest-release-images.yml"))
	allowedScript := filepath.Clean(filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1"))
	allowedWriters := map[string]bool{releaseWorkflowPath: true, latestWorkflowPath: true}
	writePattern := regexp.MustCompile(`(?i)packages:\s*write|docker/login-action|docker\s+(?:login|push|buildx[^\n]*--push)|\bdocker\s+push\b|\b(?:oras\s+(?:login|copy|push)|skopeo\s+(?:login|copy)|crane\s+(?:auth\s+login|copy|push))\b|(?:GHCR_TOKEN|CR_PAT)\s*:\s*(?:\$\{\{|\$env:|[^"'\s])|PERSONAL_ACCESS_TOKEN`)

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
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if writePattern.Match(content) && !allowedWriters[filepath.Clean(path)] && filepath.Clean(path) != allowedScript {
				t.Errorf("executable surface %s contains an undeclared registry write credential or command", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	workflow := readFile(t, releaseWorkflowPath)
	if got := strings.Count(workflow, "packages: write"); got != 1 {
		t.Fatalf("release workflow must have exactly one packages:write grant, got %d", got)
	}
	if got := strings.Count(workflow, "docker/login-action@"); got != 1 {
		t.Fatalf("release workflow must have exactly one registry login seam, got %d", got)
	}
	if got := strings.Count(workflow, "-Mode Publish"); got != 1 {
		t.Fatalf("release workflow must invoke the sole immutable publish mode exactly once, got %d", got)
	}
	if got := strings.Count(workflow, "actions/upload-artifact@"); got != 2 {
		t.Fatalf("release workflow must upload one bridge artifact and one post-logout evidence artifact, got %d", got)
	}
	if got := strings.Count(workflow, "actions/download-artifact@"); got != 1 {
		t.Fatalf("release workflow must download the bridge exactly once by artifact ID, got %d", got)
	}
	for _, forbidden := range []string{"PERSONAL_ACCESS_TOKEN", "PAT_TOKEN"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("release workflow accepts an external package credential route %q", forbidden)
		}
	}
	const rulesetSecret = "${{ secrets.MARKETPLACE_PAT }}"
	validators := []struct {
		name    string
		section string
	}{
		{
			name: "prepare-release",
			section: workflowStepSection(t, workflow,
				"Independently validate the triggering run, tag, and protected-main provenance",
				"Checkout exact validated candidate without workflow credentials"),
		},
		{
			name: "publish-images",
			section: workflowStepSection(t, workflow,
				"Revalidate workflow, tag, rulesets, and protected-main provenance on the fresh runner",
				"Confirm preparation and fresh-runner provenance agree"),
		},
	}
	for _, validator := range validators {
		if got := strings.Count(validator.section, rulesetSecret); got != 1 {
			t.Fatalf("%s provenance validator must contain exactly one ruleset secret binding, got %d", validator.name, got)
		}
		if got := strings.Count(validator.section, "-GitHubToken $env:RULESET_AUDIT_TOKEN"); got != 1 {
			t.Fatalf("%s provenance validator must contain exactly one ruleset credential consumer, got %d", validator.name, got)
		}
	}
	if got := strings.Count(workflow, rulesetSecret); got != len(validators) {
		t.Fatalf("release workflow exposes the ruleset credential outside its provenance validators, got %d references", got)
	}
	if got := strings.Count(workflow, "secrets."); got != len(validators) {
		t.Fatalf("release workflow contains an undeclared repository secret route, got %d secret references", got)
	}
	artifactCensus := workflowStepSection(t, workflow,
		"Census the current-run artifact before download",
		"Download the one censused artifact by immutable ID")
	if got := strings.Count(artifactCensus, "READ_ONLY_GITHUB_TOKEN: ${{ github.token }}"); got != 1 {
		t.Fatalf("artifact census must receive exactly one job-scoped GitHub token binding, got %d", got)
	}
	if got := strings.Count(artifactCensus, "-GitHubToken $env:READ_ONLY_GITHUB_TOKEN"); got != 1 {
		t.Fatalf("artifact census must consume exactly one job-scoped GitHub token, got %d", got)
	}
	if got := strings.Count(workflow, "READ_ONLY_GITHUB_TOKEN: ${{ github.token }}"); got != 1 {
		t.Fatalf("job-scoped GitHub token escaped the artifact census step, got %d bindings", got)
	}
	buildStart := strings.Index(workflow, "Build, scan, exercise, and export exact image data without package authority")
	if buildStart < 0 {
		t.Fatal("candidate build step start is missing")
	}
	buildEnd := strings.Index(workflow[buildStart:], "Bind the immutable bridge artifact name")
	if buildEnd < 0 {
		t.Fatal("candidate build step end is missing")
	}
	candidateBuild := workflow[buildStart : buildStart+buildEnd]
	if strings.Contains(candidateBuild, "secrets.") || !strings.Contains(candidateBuild, `RULESET_AUDIT_TOKEN: ""`) {
		t.Fatal("candidate build environment can inherit the ruleset audit credential")
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
		"push-sent", "$plan.status = 'FAIL'", "Assert-PublicationDestinationRecord",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("publication script lacks immutable-writer trust-boundary contract %q", required)
		}
	}

	latest := readFile(t, latestWorkflowPath)
	for _, required := range []string{
		"name: Promote Latest Release Images", "workflow_run:\n    workflows: [\"Release\", \"Docker Publish\"]\n    types: [completed]",
		"if: github.event.workflow_run.conclusion == 'success' && ((github.event.workflow_run.name == 'Release' && github.event.workflow_run.event == 'push') || (github.event.workflow_run.name == 'Docker Publish' && github.event.workflow_run.event == 'workflow_run'))",
		"contents: read", "actions: read", "packages: write", "Initialize isolated promotion paths", `"DOCKER_CONFIG=$dockerConfig" | Add-Content -LiteralPath $env:GITHUB_ENV`, `"RECEIPT_DIR=$receiptDir" | Add-Content -LiteralPath $env:GITHUB_ENV`, "path: ${{ env.RECEIPT_DIR }}",
		"gh api \"repos/$env:REPOSITORY_NAME/releases/latest\" --jq .tag_name", "^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$",
		"TRIGGERING_WORKFLOW_RUN_ID: ${{ github.event.workflow_run.id }}", `gh api --paginate --slurp "repos/$env:REPOSITORY_NAME/actions/runs/$triggeringWorkflowRunID/jobs?per_page=100"`, "Where-Object { $_.name -ceq 'publish-images' }", "if ($publishJobs.Count -ne 1)", "status -cne 'completed'", "conclusion -cne 'success'",
		"$maxAttempts = 6", "for ($attempt = 1; $attempt -le $maxAttempts; $attempt++)", "Start-Sleep -Seconds $retrySeconds",
		"ghcr.io/thebtf/engram", "ghcr.io/thebtf/engram-operator-console", "ghcr.io/thebtf/engram-postgres",
		"org.opencontainers.image.source", "org.opencontainers.image.version", "org.opencontainers.image.revision", "docker login ghcr.io", "docker buildx imagetools create --prefer-index=false",
		"$commitReference = \"$repository`:sha-$($release.source_commit)\"", "foreach ($field in 'manifest_digest', 'config_digest')", "release image references disagree",
		"immutable_reference = \"$repository@$manifestDigest\"", "commit_reference = $commitIdentity.reference", "docker buildx imagetools create --prefer-index=false --tag $target $image.immutable_reference",
		"$target = \"$($image.repository):latest\"", "'manifest_digest','config_digest','source','version','revision'", "docker logout ghcr.io", "latest-promotion-receipt.json", "source_commit", "triggering_workflow_name", "triggering_workflow_head_sha", "triggering_workflow_run_id", "triggering_workflow_job_conclusion", "rollout_claimed = $false",
	} {
		if !strings.Contains(latest, required) {
			t.Fatalf("latest-only promoter lacks contract %q", required)
		}
	}
	if strings.Contains(latest, "secrets.") || strings.Contains(latest, "PERSONAL_ACCESS_TOKEN") {
		t.Fatal("latest promoter must not consume repository or external personal access-token secrets")
	}
	if !strings.Contains(latest, "GH_TOKEN: ${{ github.token }}") || !strings.Contains(latest, "GITHUB_TOKEN: ${{ github.token }}") {
		t.Fatal("latest promoter must use the built-in GitHub token handoff for API and registry access")
	}
	repositoriesMatch := regexp.MustCompile(`(?s)\$repositories\s*=\s*@\(\s*'([^']+)'\s*,\s*'([^']+)'\s*,\s*'([^']+)'\s*\)`)
	repositories := repositoriesMatch.FindStringSubmatch(latest)
	if len(repositories) != 4 || repositories[1] != "ghcr.io/thebtf/engram" || repositories[2] != "ghcr.io/thebtf/engram-operator-console" || repositories[3] != "ghcr.io/thebtf/engram-postgres" {
		t.Fatalf("latest promoter must enumerate exactly the three canonical repositories, got %v", repositories[1:])
	}
	if !strings.Contains(latest, "concurrency:\n  group: engram-latest-release-images\n  cancel-in-progress: false") {
		t.Fatal("latest promoter must use the exact serialized concurrency contract")
	}
	for _, trigger := range []struct {
		name, eventName, conclusion, workflow, workflowEvent string
		allowed                                              bool
	}{
		{"manual dispatch", "workflow_dispatch", "", "", "", false},
		{"successful Release push", "workflow_run", "success", "Release", "push", true},
		{"successful Docker Publish workflow run", "workflow_run", "success", "Docker Publish", "workflow_run", true},
		{"failed Release push", "workflow_run", "failure", "Release", "push", false},
		{"Release workflow run", "workflow_run", "success", "Release", "workflow_run", false},
		{"Docker Publish push", "workflow_run", "success", "Docker Publish", "push", false},
		{"other workflow push", "workflow_run", "success", "Docker", "push", false},
	} {
		got := trigger.eventName == "workflow_run" && trigger.conclusion == "success" && ((trigger.workflow == "Release" && trigger.workflowEvent == "push") || (trigger.workflow == "Docker Publish" && trigger.workflowEvent == "workflow_run"))
		if got != trigger.allowed {
			t.Errorf("promotion guard accepts %s = %t, want %t", trigger.name, got, trigger.allowed)
		}
	}
	if strings.Contains(latest, "workflows: [\"Release\"]") {
		t.Fatal("latest promoter must also trigger after Docker Publish completes")
	}
	jobEnvStart := strings.Index(latest, "    env:\n      REPOSITORY_NAME:")
	stepsStart := strings.Index(latest, "    steps:")
	if jobEnvStart < 0 || stepsStart <= jobEnvStart || strings.Contains(latest[jobEnvStart:stepsStart], "runner.") {
		t.Fatal("latest promoter job environment must not resolve runner context")
	}
	initializerIndex := strings.Index(latest, "Initialize isolated promotion paths")
	firstActionIndex := strings.Index(latest, "uses:")
	if initializerIndex < 0 || firstActionIndex < 0 || initializerIndex > firstActionIndex {
		t.Fatal("latest promoter must initialize isolated paths before every action step")
	}
	if got := strings.Count(latest, "packages: write"); got != 1 {
		t.Fatalf("latest promoter must have exactly one packages:write grant, got %d", got)
	}
	if got := strings.Count(latest, "docker login ghcr.io"); got != 1 {
		t.Fatalf("latest promoter must have exactly one registry login, got %d", got)
	}
	if got := len(regexp.MustCompile(`(?m)^\s*docker buildx imagetools create\b`).FindAllString(latest, -1)); got != 1 {
		t.Fatalf("latest promoter must have exactly one registry-native promotion invocation, got %d", got)
	}
	if got := strings.Count(latest, "docker buildx imagetools create --prefer-index=false --tag $target $image.immutable_reference"); got != 1 {
		t.Fatalf("latest promoter must use exactly one canonical registry-native promotion seam, got %d", got)
	}
	waitIndex := strings.Index(latest, "Wait for all public immutable release images before login")
	loginIndex := strings.Index(latest, "Login to GHCR only after immutable provenance inspection")
	promoteIndex := strings.Index(latest, "Promote each official release image to latest and read it back")
	if !(waitIndex >= 0 && waitIndex < loginIndex && loginIndex < promoteIndex) {
		t.Fatalf("latest promoter waits for exact official images before login and writes only afterwards: wait=%d login=%d promote=%d", waitIndex, loginIndex, promoteIndex)
	}
	publishJobsIndex := strings.Index(latest, `gh api --paginate --slurp "repos/$env:REPOSITORY_NAME/actions/runs/$triggeringWorkflowRunID/jobs?per_page=100"`)
	if publishJobsIndex < 0 || publishJobsIndex >= loginIndex {
		t.Fatal("latest promoter must REST-validate the triggering Docker Publish publish-images job before registry login")
	}
	releaseHeadGuard := "if ($triggeringWorkflowName -eq 'Release' -and $commit -cne $triggeringWorkflowHeadSHA)"
	releaseHeadGuardIndex := strings.Index(latest, releaseHeadGuard)
	if releaseHeadGuardIndex < 0 || releaseHeadGuardIndex >= loginIndex {
		t.Fatal("latest promoter must reject only a Release workflow_run whose head differs from the latest release commit before registry login")
	}
	if strings.Contains(latest, "if ($commit -cne $triggeringWorkflowHeadSHA)") {
		t.Fatal("latest promoter must not apply the Release head check to Docker Publish")
	}
	if strings.Contains(latest, "workflow_dispatch:") || strings.Contains(latest, "github.event_name == 'workflow_dispatch'") {
		t.Fatal("latest promoter must not retain a manual-dispatch path")
	}
	for _, required := range []string{
		"gh api \"repos/$env:REPOSITORY_NAME/releases/latest\" --jq .tag_name",
		"$commitReference = \"$repository`:sha-$($release.source_commit)\"",
		"foreach ($field in 'manifest_digest', 'config_digest')", "release image references disagree",
	} {
		index := strings.Index(latest, required)
		if index < 0 || index >= loginIndex {
			t.Fatalf("latest promoter must validate Docker Publish through latest-release v/sha OCI equality before registry login: %q", required)
		}
	}
	promotionReceipt := "" +
		"            $latest.Add($observed)\n" +
		"            [ordered]@{ images = $latest } |\n" +
		"              ConvertTo-Json -Depth 5 |\n" +
		"              Set-Content -LiteralPath (Join-Path $env:RECEIPT_DIR 'latest.json') -Encoding utf8NoBOM"
	if !strings.Contains(latest, "$latest = [System.Collections.Generic.List[object]]::new()") || !strings.Contains(latest, promotionReceipt) {
		t.Fatal("latest promoter must write latest.json after each successful promotion so the always receipt retains partial success")
	}
	for _, step := range []string{
		"Logout and erase the isolated registry credential directory",
		"Write latest-promotion receipt",
		"Upload latest-promotion receipt",
	} {
		stepPattern := regexp.MustCompile(`(?ms)^\s*- name:\s*` + regexp.QuoteMeta(step) + `\s*$.*?(?:^\s*- name:|\z)`)
		matches := stepPattern.FindAllString(latest, -1)
		if len(matches) != 1 || !regexp.MustCompile(`(?m)^\s*if:\s*always\(\)\s*$`).MatchString(matches[0]) {
			t.Fatalf("latest promoter %q must have exactly one step block with if: always()", step)
		}
	}
	if !strings.Contains(latest, "TRIGGERING_WORKFLOW_HEAD_SHA: ${{ github.event.workflow_run.head_sha }}") ||
		!strings.Contains(latest, "if ($env:GITHUB_EVENT_NAME -ne 'workflow_run')") {
		t.Fatal("latest promoter must bind and require the triggering workflow head")
	}
	for _, forbidden := range []string{
		"release:\n", "github.event.release", "actions/checkout@", "docker/login-action@", "docker build ", "docker buildx build", "docker buildx bake", "docker push", "docker manifest create", "docker manifest push", "oras login", "oras copy", "oras push", "skopeo login", "skopeo copy", "crane auth login", "crane copy", "crane push", "GHCR_TOKEN", "CR_PAT", ":main", "ssh", "production",
	} {
		if strings.Contains(latest, forbidden) {
			t.Fatalf("latest promoter exposes a forbidden trigger, mutable source, build, or production surface %q", forbidden)
		}
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

func workflowStepSection(t *testing.T, workflow, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(workflow, startMarker)
	if start < 0 {
		t.Fatalf("workflow step start marker is missing: %q", startMarker)
	}
	end := strings.Index(workflow[start:], endMarker)
	if end <= 0 {
		t.Fatalf("workflow step end marker is missing or out of order: %q", endMarker)
	}
	return workflow[start : start+end]
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
		{name: "tag bypass policy omitted", mutate: func(f map[string]any) { delete(f["tag_rulesets"].([]map[string]any)[0], "bypass_actors") }},
		{name: "recovery bypass policy omitted", mutate: func(f map[string]any) { delete(branchRuleset(f), "bypass_actors") }},
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
		{name: "missing SARIF", mutate: func(root string) {
			if err := os.Remove(filepath.Join(root, "server.trivy.sarif")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "SARIF hash mismatch", mutate: func(root string) {
			if err := os.WriteFile(filepath.Join(root, "server.trivy.sarif"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed SARIF shape with matching hash", mutate: func(root string) {
			path := filepath.Join(root, "server.trivy.sarif")
			writeJSONAt(t, path, map[string]any{"version": "2.1.0", "runs": map[string]any{}})
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			manifest := readJSONMap(t, filepath.Join(root, "final-image-set.json"))
			manifest["sarif_sha256"].(map[string]any)["server"] = sha256Hex(data)
			writeJSONAt(t, filepath.Join(root, "final-image-set.json"), manifest)
			updatePayloadManifestHash(t, root)
		}},
		{name: "nonzero finding count", mutate: func(root string) {
			manifest := readJSONMap(t, filepath.Join(root, "final-image-set.json"))
			manifest["high_critical_findings"].(map[string]any)["server"] = 1
			writeJSONAt(t, filepath.Join(root, "final-image-set.json"), manifest)
			updatePayloadManifestHash(t, root)
		}},
		{name: "incomplete runtime proof", mutate: func(root string) {
			manifest := readJSONMap(t, filepath.Join(root, "final-image-set.json"))
			manifest["runtime_proof"].(map[string]any)["critical_tests"] = false
			writeJSONAt(t, filepath.Join(root, "final-image-set.json"), manifest)
			updatePayloadManifestHash(t, root)
		}},
		{name: "retained cleanup resource", mutate: func(root string) {
			manifest := readJSONMap(t, filepath.Join(root, "final-image-set.json"))
			manifest["cleanup"].(map[string]any)["containers"] = []any{"leaked-container"}
			writeJSONAt(t, filepath.Join(root, "final-image-set.json"), manifest)
			updatePayloadManifestHash(t, root)
		}},
		{name: "scanner exception input", mutate: func(root string) {
			manifest := readJSONMap(t, filepath.Join(root, "final-image-set.json"))
			manifest["scanner_exception_inputs"] = []any{"CVE-test"}
			writeJSONAt(t, filepath.Join(root, "final-image-set.json"), manifest)
			updatePayloadManifestHash(t, root)
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
	publicationCases := []struct {
		name   string
		pass   bool
		mutate func(string)
	}{
		{name: "retained acceptance proof", pass: true},
		{name: "retained SARIF mismatch", mutate: func(root string) {
			if err := os.WriteFile(filepath.Join(root, "postgres.trivy.sarif"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained manifest semantic tamper", mutate: func(root string) {
			manifestPath := filepath.Join(root, "final-image-set.json")
			manifest := readJSONMap(t, manifestPath)
			manifest["runtime_proof"].(map[string]any)["critical_tests"] = false
			writeJSONAt(t, manifestPath, manifest)
			manifestData, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			digest := "sha256:" + sha256Hex(manifestData)
			for _, name := range []string{"payload-validation.json", "pre-login-publication-plan.json", "publication-result.json"} {
				path := filepath.Join(root, name)
				record := readJSONMap(t, path)
				if name == "payload-validation.json" {
					record["manifest_sha256"] = digest
				} else {
					record["acceptance_manifest_sha256"] = digest
				}
				writeJSONAt(t, path, record)
			}
		}},
		{name: "noncanonical destination", mutate: func(root string) {
			for _, name := range []string{"pre-login-publication-plan.json", "publication-result.json"} {
				path := filepath.Join(root, name)
				record := readJSONMap(t, path)
				record["destinations"].([]any)[0].(map[string]any)["reference"] = "ghcr.io/thebtf/unrelated:v6.43.0-rc.1"
				writeJSONAt(t, path, record)
			}
		}},
		{name: "wrong destination image digest", mutate: func(root string) {
			path := filepath.Join(root, "publication-result.json")
			record := readJSONMap(t, path)
			record["destinations"].([]any)[0].(map[string]any)["config_digest"] = "sha256:" + strings.Repeat("f", 64)
			writeJSONAt(t, path, record)
		}},
		{name: "credential directory retained", mutate: func(root string) {
			path := publicationEvidenceCredentialPath(root)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(path) })
		}},
	}
	for _, test := range publicationCases {
		test := test
		t.Run("publication evidence "+test.name, func(t *testing.T) {
			root := writePublicationEvidenceFixture(t, repo, sha, "v6.43.0-rc.1")
			if test.mutate != nil {
				test.mutate(root)
			}
			runImageGate(t, repo, test.pass,
				"-Mode", "ValidatePublicationEvidence",
				"-TrustedOutputRoot", publicationEvidenceTrustedRoot(root),
				"-EvidenceRoot", root,
				"-CredentialDirectoryPath", publicationEvidenceCredentialPath(root))
		})
	}
}

func writeReleasePayloadFixture(t *testing.T, commit, version string) string {
	t.Helper()
	root := t.TempDir()
	ids := map[string]string{
		"server":           "sha256:" + strings.Repeat("1", 64),
		"operator_console": "sha256:" + strings.Repeat("2", 64),
		"postgres":         "sha256:" + strings.Repeat("3", 64),
	}
	sarifFiles := map[string]string{
		"server":           "server.trivy.sarif",
		"operator_console": "operator-console.trivy.sarif",
		"postgres":         "postgres.trivy.sarif",
	}
	sarifHashes := make(map[string]string, len(sarifFiles))
	for name, filename := range sarifFiles {
		path := filepath.Join(root, filename)
		writeJSONAt(t, path, map[string]any{
			"version": "2.1.0",
			"runs": []any{map[string]any{
				"tool":    map[string]any{"driver": map[string]any{"name": "fixture-" + name}},
				"results": []any{},
			}},
		})
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sarifHashes[name] = sha256Hex(data)
	}
	manifestPath := filepath.Join(root, "final-image-set.json")
	writeJSONAt(t, manifestPath, map[string]any{
		"schema_version": 1, "status": "PASS", "source_parent_commit": commit,
		"build_version": version, "source_worktree_dirty": false,
		"build_context": "git-archive-tracked-files-only",
		"git_credentials_present_in_build_context": false,
		"build_context_cleanup_passed":             true, "no_allowlist": true,
		"scanner_exception_inputs": []any{}, "failure": nil,
		"image_ids": ids, "high_critical_findings": map[string]any{
			"server": 0, "operator_console": 0, "postgres": 0,
		},
		"sarif_sha256": sarifHashes,
		"runtime_proof": map[string]any{
			"critical_tests": true, "volume_ownership_contract": true,
			"server_home_persistence": true, "legacy_postgres_uid_migration": true,
			"compose_all_healthy": true, "server_liveness": true,
			"server_readiness": true, "operator_readiness": true,
			"postgres_17_10": true, "pgvector_0_8_1": true,
			"migrations_present": true, "migration_table_count": 40,
			"core_schema_table_count": 6, "restart_recovery": true,
			"postgres_recreation_retained_marker": true,
			"local_tags_promoted_from_exact_ids":  true,
		},
		"cleanup": map[string]any{
			"status": "PASS", "containers": []any{}, "volumes": []any{}, "networks": []any{},
		},
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

func updatePayloadManifestHash(t *testing.T, root string) {
	t.Helper()
	manifestData, err := os.ReadFile(filepath.Join(root, "final-image-set.json"))
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "release-bundle.json")
	bundle := readJSONMap(t, bundlePath)
	bundleManifest := bundle["manifest"].(map[string]any)
	bundleManifest["sha256"] = sha256Hex(manifestData)
	bundleManifest["size_bytes"] = len(manifestData)
	writeJSONAt(t, bundlePath, bundle)
}

func writePublicationEvidenceFixture(t *testing.T, repo, commit, version string) string {
	t.Helper()
	payloadRoot := writeReleasePayloadFixture(t, commit, version)
	trustedRoot := os.Getenv("RUNNER_TEMP")
	if trustedRoot == "" {
		trustedRoot = t.TempDir()
	}
	evidenceRoot, err := os.MkdirTemp(trustedRoot, "engram-publication-evidence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(evidenceRoot) })
	runImageGate(t, repo, true,
		"-Mode", "ValidatePayload",
		"-PayloadRoot", payloadRoot,
		"-ExpectedSha", commit,
		"-ReleaseVersion", version,
		"-OutputPath", filepath.Join(evidenceRoot, "payload-validation.json"))
	for _, name := range []string{"final-image-set.json", "server.trivy.sarif", "operator-console.trivy.sarif", "postgres.trivy.sarif"} {
		data, err := os.ReadFile(filepath.Join(payloadRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(evidenceRoot, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	payloadValidation := readJSONMap(t, filepath.Join(evidenceRoot, "payload-validation.json"))
	manifestDigest := payloadValidation["manifest_sha256"].(string)
	manifest := readJSONMap(t, filepath.Join(evidenceRoot, "final-image-set.json"))
	ids := manifest["image_ids"].(map[string]any)
	writeJSONAt(t, filepath.Join(evidenceRoot, "artifact-census.json"), map[string]any{
		"artifact_count": 1,
	})
	preflightDestinations := make([]map[string]any, 0, 6)
	publicationDestinations := make([]map[string]any, 0, 6)
	for index, image := range []struct {
		name       string
		repository string
		digest     string
	}{
		{"server", "ghcr.io/thebtf/engram", ids["server"].(string)},
		{"operator_console", "ghcr.io/thebtf/engram-operator-console", ids["operator_console"].(string)},
		{"postgres", "ghcr.io/thebtf/engram-postgres", ids["postgres"].(string)},
	} {
		for _, tag := range []string{version, "sha-" + commit} {
			reference := image.repository + ":" + tag
			preflightDestinations = append(preflightDestinations, map[string]any{
				"image": image.name, "reference": reference, "config_digest": image.digest,
				"action": "inspect-after-login", "manifest_digest": nil,
			})
			publicationDestinations = append(publicationDestinations, map[string]any{
				"image": image.name, "reference": reference, "config_digest": image.digest,
				"action": "pushed", "manifest_digest": fmt.Sprintf("sha256:%064x", index+1),
			})
		}
	}
	writeJSONAt(t, filepath.Join(evidenceRoot, "pre-login-publication-plan.json"), map[string]any{
		"schema_version": 1, "release_version": version, "source_commit": commit,
		"single_writer_model":                   "repository-workflow-release-publish",
		"external_package_admin_trust_boundary": true,
		"remote_inspection":                     "deferred-until-authenticated-publish",
		"acceptance_manifest_sha256":            manifestDigest,
		"destinations":                          preflightDestinations,
	})
	writeJSONAt(t, filepath.Join(evidenceRoot, "publication-result.json"), map[string]any{
		"schema_version": 1, "release_version": version, "source_commit": commit,
		"single_writer_model":                   "repository-workflow-release-publish",
		"external_package_admin_trust_boundary": true,
		"remote_inspection":                     "complete",
		"acceptance_manifest_sha256":            manifestDigest,
		"status":                                "PASS", "failure": nil,
		"destinations": publicationDestinations,
	})
	return evidenceRoot
}

func publicationEvidenceTrustedRoot(evidenceRoot string) string {
	if root := os.Getenv("RUNNER_TEMP"); root != "" {
		return root
	}
	return filepath.Dir(evidenceRoot)
}

func publicationEvidenceCredentialPath(evidenceRoot string) string {
	return filepath.Join(publicationEvidenceTrustedRoot(evidenceRoot), filepath.Base(evidenceRoot)+"-docker-auth")
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
		{name: "bypass policy omitted", fixture: mutateRuleset(positive, "delete-bypass", nil)},
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

func testLiveRulesetAPIArray(t *testing.T, repo string) {
	t.Helper()
	const token = "ruleset-audit-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("ruleset API received wrong authorization header")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/thebtf/engram/rulesets":
			if r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
				t.Errorf("ruleset API received unexpected pagination query %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 9001, "target": "tag", "enforcement": "active"},
				{"id": 9100, "target": "branch", "enforcement": "active"},
			})
		case "/repos/thebtf/engram/rulesets/9001":
			_ = json.NewEncoder(w).Encode(exactRulesetFixture()[0])
		case "/repos/thebtf/engram/rulesets/9100":
			_ = json.NewEncoder(w).Encode(exactBranchRulesetFixture()[0])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	sha := strings.Repeat("d", 40)
	runImageGate(t, repo, true,
		"-Mode", "ValidateRelease",
		"-ReleaseRef", "refs/tags/v6.46.0",
		"-ExpectedSha", sha,
		"-ActualSha", sha,
		"-Repository", "thebtf/engram",
		"-GitHubToken", token,
		"-GitHubApiUrl", server.URL)

	gitFixture := newGitRefFreshnessFixture(t)
	eventPath := writeJSONFixture(t, workflowRunFixture(gitFixture.initialSHA, "v1.0.0")["event"])
	runImageGate(t, repo, true,
		"-Mode", "ValidateWorkflowRun",
		"-EventOnlyValidation",
		"-RepositoryRoot", gitFixture.consumer,
		"-WorkflowRunEventPath", eventPath,
		"-Repository", "thebtf/engram",
		"-GitHubToken", token,
		"-GitHubApiUrl", server.URL)
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
	expectedRefs := expectedPublicationRefs("ghcr.io", "thebtf/engram", version, commit)
	t.Run("credential-free plan defers registry inspection", func(t *testing.T) {
		var requests atomic.Int64
		registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(registry.Close)

		outputPath := filepath.Join(t.TempDir(), "plan.json")
		runImageGateWithoutDocker(t, repo,
			"-Mode", "PlanPublication", "-ManifestPath", manifest, "-ReleaseVersion", version,
			"-Registry", registry.URL, "-Repository", "thebtf/engram", "-OutputPath", outputPath)
		if got := requests.Load(); got != 0 {
			t.Fatalf("credential-free publication plan made %d registry requests, want 0", got)
		}
		assertDeferredPublicationPlan(t, outputPath,
			expectedPublicationRefs(registry.URL, "thebtf/engram", version, commit))
	})

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

	t.Run("registry inspection failure retains completed evidence", func(t *testing.T) {
		trustedRoot := t.TempDir()
		manifestPath := filepath.Join(trustedRoot, "final-image-set.json")
		manifestData, err := json.Marshal(map[string]any{
			"status": "PASS", "source_parent_commit": commit, "image_ids": ids,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
			t.Fatal(err)
		}
		stubDir := filepath.Join(trustedRoot, "bin")
		if err := os.MkdirAll(stubDir, 0o700); err != nil {
			t.Fatal(err)
		}
		writePublishedScanFixtureCommand(t, stubDir, "docker", `
param([Parameter(ValueFromRemainingArguments = $true)][string[]]$ToolArguments)
if ($ToolArguments -contains 'imagetools') { 'registry unavailable'; exit 1 }
exit 97
`)
		outputPath := filepath.Join(trustedRoot, "publication.json")
		script := filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1")
		command := exec.Command("pwsh",
			"-NoProfile", "-File", script,
			"-Mode", "Publish",
			"-ManifestPath", manifestPath,
			"-ReleaseVersion", version,
			"-ExpectedSha", commit,
			"-Registry", "ghcr.io",
			"-Repository", "thebtf/engram",
			"-OutputPath", outputPath,
			"-TrustedOutputRoot", trustedRoot,
		)
		env := os.Environ()
		for index, entry := range env {
			name, value, ok := strings.Cut(entry, "=")
			if !ok || !strings.EqualFold(name, "PATH") {
				continue
			}
			env[index] = name + "=" + stubDir + string(os.PathListSeparator) + value
			break
		}
		command.Env = append(env, "RUNNER_TEMP="+trustedRoot)
		output, commandErr := command.CombinedOutput()
		if commandErr == nil {
			t.Fatalf("publication unexpectedly succeeded with failed registry inspection:\n%s", output)
		}
		data, readErr := os.ReadFile(outputPath)
		if readErr != nil {
			t.Fatalf("publication failure lost its evidence file: %v\n%s", readErr, output)
		}
		var evidence struct {
			Status      string `json:"status"`
			Failure     string `json:"failure"`
			CompletedAt string `json:"completed_at"`
		}
		if err := json.Unmarshal(data, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.Status != "FAIL" || !strings.Contains(evidence.Failure, "Remote image inspection failed") {
			t.Fatalf("publication registry failure evidence lost status or cause: %+v", evidence)
		}
		if _, err := time.Parse(time.RFC3339Nano, evidence.CompletedAt); err != nil {
			t.Fatalf("publication failure evidence lacks a valid completion timestamp %q: %v", evidence.CompletedAt, err)
		}
	})
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

type publishedImageScanSummary struct {
	Status                    string                         `json:"status"`
	ReleaseVersion            string                         `json:"release_version"`
	SourceCommit              string                         `json:"source_commit"`
	NoAllowlist               bool                           `json:"no_allowlist"`
	TotalHighCriticalFindings int                            `json:"total_high_critical_findings"`
	Images                    []publishedImageScanEvidence   `json:"images"`
	TrivyDatabase             publishedTrivyDatabaseEvidence `json:"trivy_database"`
}

type publishedTrivyDatabaseEvidence struct {
	RefreshLog       string `json:"refresh_log"`
	VersionLog       string `json:"version_log"`
	VersionLogSHA256 string `json:"version_log_sha256"`
}

type publishedImageScanEvidence struct {
	Name                 string  `json:"name"`
	TagReference         string  `json:"tag_reference"`
	CommitReference      string  `json:"commit_reference"`
	ConfigDigest         string  `json:"config_digest"`
	ManifestDigest       string  `json:"manifest_digest"`
	CommitManifestDigest string  `json:"commit_manifest_digest"`
	ScanReference        string  `json:"scan_reference"`
	Sarif                string  `json:"sarif"`
	Log                  string  `json:"log"`
	ScanExitCode         *int    `json:"scan_exit_code"`
	HighCriticalFindings *int    `json:"high_critical_findings"`
	Error                *string `json:"error"`
}

func testPublishedImageScanGate(t *testing.T, repo string) {
	t.Helper()
	sourceCommit := strings.Repeat("a", 40)
	t.Run("no allowlist is mandatory", func(t *testing.T) {
		output := runImageGate(t, repo, false,
			"-Mode", "ScanPublished",
			"-RepositoryRoot", t.TempDir(),
			"-ReleaseVersion", "v6.47.6",
			"-ExpectedSha", sourceCommit,
			"-Registry", "ghcr.io",
			"-Repository", "thebtf/engram",
			"-ArtifactRoot", "evidence")
		if !strings.Contains(output, "-NoAllowlist is mandatory") {
			t.Fatalf("published scan did not reject missing no-allowlist guard: %s", output)
		}
	})
	t.Run("only final canonical release tags scan", func(t *testing.T) {
		output := runImageGate(t, repo, false,
			"-Mode", "ScanPublished",
			"-RepositoryRoot", t.TempDir(),
			"-ReleaseVersion", "v6.47.6-rc.1",
			"-ExpectedSha", sourceCommit,
			"-Registry", "ghcr.io",
			"-Repository", "thebtf/engram",
			"-ArtifactRoot", "evidence",
			"-NoAllowlist")
		if !strings.Contains(output, "canonical final vMAJOR.MINOR.PATCH") {
			t.Fatalf("published scan accepted a non-final release version: %s", output)
		}
	})
	for _, test := range []struct {
		name         string
		mode         string
		wantSuccess  bool
		wantFindings int
		wantScans    int
	}{
		{name: "clean immutable digests pass", wantSuccess: true, wantScans: 3},
		{name: "all image findings aggregate after all scans", mode: "findings", wantFindings: 3, wantScans: 3},
		{name: "scanner failure remains distinct and fail closed", mode: "scanner-failure", wantScans: 3},
		{name: "release tag movement fails closed against commit alias", mode: "identity-mismatch", wantScans: 2},
		{name: "database refresh failure skips registry resolution", mode: "database-failure"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			artifactPath, dockerLog, trivyLog, output, err := runPublishedImageScanFixture(t, repo, test.mode)
			if test.wantSuccess && err != nil {
				t.Fatalf("published image scan unexpectedly failed: %v\n%s", err, output)
			}
			if !test.wantSuccess && err == nil {
				t.Fatalf("published image scan unexpectedly passed:\n%s", output)
			}

			var summary publishedImageScanSummary
			data, readErr := os.ReadFile(filepath.Join(artifactPath, "published-image-scan-summary.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if unmarshalErr := json.Unmarshal(data, &summary); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			wantStatus := "FAIL"
			if test.wantSuccess {
				wantStatus = "PASS"
			}
			if summary.Status != wantStatus || summary.ReleaseVersion != "v6.47.6" || summary.SourceCommit != sourceCommit || !summary.NoAllowlist {
				t.Fatalf("published scan summary status/version/commit/allowlist = %q/%q/%q/%v, want %q/v6.47.6/%s/true", summary.Status, summary.ReleaseVersion, summary.SourceCommit, summary.NoAllowlist, wantStatus, sourceCommit)
			}
			if test.mode == "database-failure" {
				if summary.TrivyDatabase.VersionLogSHA256 != "" {
					t.Fatalf("failed database refresh retained nonexistent version evidence: %+v", summary.TrivyDatabase)
				}
			} else {
				versionLogPath := filepath.Join(artifactPath, summary.TrivyDatabase.VersionLog)
				versionLogBytes, versionLogErr := os.ReadFile(versionLogPath)
				if versionLogErr != nil {
					t.Fatal(versionLogErr)
				}
				if summary.TrivyDatabase.RefreshLog != "trivy-db-refresh.log" || summary.TrivyDatabase.VersionLog != "trivy-version.log" ||
					summary.TrivyDatabase.VersionLogSHA256 != "sha256:"+sha256Hex(versionLogBytes) {
					t.Fatalf("published scan does not retain post-refresh Trivy version evidence: %+v", summary.TrivyDatabase)
				}
			}
			if summary.TotalHighCriticalFindings != test.wantFindings || len(summary.Images) != 3 {
				t.Fatalf("published scan summary findings/images = %d/%d, want %d/3", summary.TotalHighCriticalFindings, len(summary.Images), test.wantFindings)
			}
			wantTags := map[string]string{
				"server":           "ghcr.io/thebtf/engram:v6.47.6",
				"operator-console": "ghcr.io/thebtf/engram-operator-console:v6.47.6",
				"postgres":         "ghcr.io/thebtf/engram-postgres:v6.47.6",
			}
			for _, image := range summary.Images {
				wantTag, ok := wantTags[image.Name]
				if !ok || image.TagReference != wantTag {
					t.Fatalf("published scan retained non-canonical tag reference %q for %q", image.TagReference, image.Name)
				}
				wantRepository := strings.Split(image.TagReference, ":v")[0]
				if image.CommitReference != wantRepository+":sha-"+sourceCommit {
					t.Fatalf("published scan retained wrong commit alias for %s: %+v", image.Name, image)
				}
				if _, statErr := os.Stat(filepath.Join(artifactPath, image.Log)); statErr != nil {
					t.Fatalf("published scan lacks %s log: %v", image.Name, statErr)
				}
				if test.mode == "database-failure" {
					if image.Error == nil || !strings.Contains(*image.Error, "fresh vulnerability database") || image.ManifestDigest != "" || image.CommitManifestDigest != "" || image.ScanReference != "" {
						t.Fatalf("database failure did not skip registry identity resolution for %s: %+v", image.Name, image)
					}
					continue
				}
				if test.mode == "identity-mismatch" && image.Name == "server" {
					if image.Error == nil || !strings.Contains(*image.Error, "different immutable images") || image.ManifestDigest != "sha256:"+strings.Repeat("d", 64) || image.CommitManifestDigest != "sha256:"+strings.Repeat("e", 64) || image.ScanReference != "" {
						t.Fatalf("release tag movement was not retained as an identity mismatch: %+v", image)
					}
					continue
				}
				if image.ConfigDigest != "sha256:"+strings.Repeat("c", 64) || image.ManifestDigest != "sha256:"+strings.Repeat("d", 64) || image.CommitManifestDigest != image.ManifestDigest || image.ScanReference != wantRepository+"@sha256:"+strings.Repeat("d", 64) {
					t.Fatalf("published scan did not bind %s release and commit identities to one immutable manifest: %+v", image.Name, image)
				}
				if test.mode != "scanner-failure" || image.Name != "postgres" {
					if _, statErr := os.Stat(filepath.Join(artifactPath, image.Sarif)); statErr != nil {
						t.Fatalf("published scan lacks %s SARIF: %v", image.Name, statErr)
					}
				}
				if test.mode == "findings" && (image.HighCriticalFindings == nil || *image.HighCriticalFindings != 1 || image.ScanExitCode == nil || *image.ScanExitCode != 1) {
					t.Fatalf("published scan did not retain finding evidence for %s: %+v", image.Name, image)
				}
			}
			if test.mode == "scanner-failure" {
				postgres := summary.Images[2]
				if postgres.Error == nil || !strings.Contains(*postgres.Error, "Trivy scanner failure") {
					t.Fatalf("published scanner failure was not distinguished in summary: %+v", postgres)
				}
			}

			dockerBytes, dockerErr := os.ReadFile(dockerLog)
			if test.mode == "database-failure" {
				if dockerErr == nil && len(strings.TrimSpace(string(dockerBytes))) != 0 {
					t.Fatalf("database failure still resolved registry identities:\n%s", dockerBytes)
				}
				if dockerErr != nil && !os.IsNotExist(dockerErr) {
					t.Fatal(dockerErr)
				}
			} else {
				if dockerErr != nil || len(strings.TrimSpace(string(dockerBytes))) == 0 {
					t.Fatalf("published scan did not resolve release and commit identities: %v", dockerErr)
				}
				forbiddenVerbs := map[string]struct{}{"login": {}, "pull": {}, "push": {}, "run": {}}
				for _, line := range strings.Split(strings.TrimSpace(string(dockerBytes)), "\n") {
					fields := strings.Fields(strings.TrimSpace(line))
					if len(fields) == 0 {
						continue
					}
					if _, forbidden := forbiddenVerbs[fields[0]]; forbidden {
						t.Fatalf("published scan invoked Docker mutation or candidate execution %q", fields[0])
					}
				}
			}
			trivyInvocations := readFile(t, trivyLog)
			if strings.Count(trivyInvocations, "--download-db-only") != 1 || strings.Count(trivyInvocations, "--skip-db-update") != test.wantScans {
				t.Fatalf("published scan refresh/scan count mismatch, want 1/%d:\n%s", test.wantScans, trivyInvocations)
			}
			if strings.Count(trivyInvocations, "@sha256:"+strings.Repeat("d", 64)) != test.wantScans {
				t.Fatalf("published scan did not scan %d expected immutable digest references:\n%s", test.wantScans, trivyInvocations)
			}
		})
	}
}

func runPublishedImageScanFixture(t *testing.T, repo, mode string) (artifactPath, dockerLog, trivyLog string, output []byte, commandErr error) {
	t.Helper()
	root := t.TempDir()
	stubDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(stubDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writePublishedScanFixtureCommand(t, stubDir, "docker", `
param([Parameter(ValueFromRemainingArguments = $true)][string[]]$ToolArguments)
[IO.File]::AppendAllText($env:FAKE_DOCKER_LOG, ($ToolArguments -join [char]9) + [Environment]::NewLine)
$reference = $ToolArguments | Where-Object { $_ -match '^ghcr\.io/' } | Select-Object -Last 1
$movedServerAlias = $env:FAKE_TRIVY_MODE -eq 'identity-mismatch' -and $reference -match '^ghcr\.io/thebtf/engram:sha-'
if ($ToolArguments -contains '--raw') {
  if ($movedServerAlias) { '{"schemaVersion":2,"config":{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}' }
  else { '{"schemaVersion":2,"config":{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}' }
  exit 0
}
if ($ToolArguments -contains '--format') {
  if ($movedServerAlias) { '{"digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}' }
  else { '{"digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}' }
  exit 0
}
exit 97
`)
	writePublishedScanFixtureCommand(t, stubDir, "trivy", `
param([Parameter(ValueFromRemainingArguments = $true)][string[]]$ToolArguments)
[IO.File]::AppendAllText($env:FAKE_TRIVY_LOG, ($ToolArguments -join [char]9) + [Environment]::NewLine)
if ($ToolArguments -contains '--version') { 'Version: fixture'; exit 0 }
if ($ToolArguments -contains '--download-db-only') {
  if ($env:FAKE_TRIVY_MODE -eq 'database-failure') { exit 2 }
  exit 0
}
$reference = $ToolArguments[$ToolArguments.Count - 1]
if ($env:FAKE_TRIVY_MODE -eq 'scanner-failure' -and $reference -match '-postgres@') { exit 2 }
$outputIndex = [Array]::IndexOf($ToolArguments, '--output')
if ($outputIndex -lt 0) { exit 98 }
$results = @()
if ($env:FAKE_TRIVY_MODE -eq 'findings') { $results = @([ordered]@{ ruleId = 'CVE-test' }) }
[ordered]@{ version = '2.1.0'; runs = @([ordered]@{ tool = [ordered]@{ driver = [ordered]@{ name = 'fixture-trivy' } }; results = $results }) } |
  ConvertTo-Json -Depth 10 | Set-Content -Encoding utf8NoBOM -LiteralPath $ToolArguments[$outputIndex + 1]
if ($results.Count -ne 0) { exit 1 }
exit 0
`)

	dockerLog = filepath.Join(root, "docker.log")
	trivyLog = filepath.Join(root, "trivy.log")
	artifactPath = filepath.Join(root, "evidence")
	script := filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1")
	commandArgs := []string{
		"-NoProfile", "-File", script,
		"-Mode", "ScanPublished",
		"-RepositoryRoot", root,
		"-ReleaseVersion", "v6.47.6",
		"-ExpectedSha", strings.Repeat("a", 40),
		"-Registry", "ghcr.io",
		"-Repository", "thebtf/engram",
		"-ArtifactRoot", "evidence",
		"-Platform", "linux/amd64",
		"-NoAllowlist",
	}
	command := exec.Command("pwsh", commandArgs...)
	env := os.Environ()
	pathSet := false
	for i, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.EqualFold(name, "PATH") {
			continue
		}
		if value == "" {
			env[i] = name + "=" + stubDir
		} else {
			env[i] = name + "=" + stubDir + string(os.PathListSeparator) + value
		}
		pathSet = true
		break
	}
	if !pathSet {
		env = append(env, "PATH="+stubDir)
	}
	command.Env = append(env,
		"FAKE_DOCKER_LOG="+dockerLog,
		"FAKE_TRIVY_LOG="+trivyLog,
		"FAKE_TRIVY_MODE="+mode,
	)
	output, commandErr = command.CombinedOutput()
	return artifactPath, dockerLog, trivyLog, output, commandErr
}

func writePublishedScanFixtureCommand(t *testing.T, directory, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name+".ps1"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	launcherName := name
	launcher := "#!/bin/sh\nexec pwsh -NoProfile -File \"$(dirname \"$0\")/" + name + ".ps1\" \"$@\"\n"
	if runtime.GOOS == "windows" {
		launcherName += ".cmd"
		launcher = "@echo off\r\npwsh -NoProfile -File \"%~dp0" + name + ".ps1\" %*\r\n"
	}
	if err := os.WriteFile(filepath.Join(directory, launcherName), []byte(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
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

func runImageGateWithoutDocker(t *testing.T, repo string, args ...string) string {
	t.Helper()
	stubDir := t.TempDir()
	dockerName := "docker"
	dockerStub := "#!/bin/sh\nexit 97\n"
	if runtime.GOOS == "windows" {
		dockerName = "docker.cmd"
		dockerStub = "@echo off\r\nexit /b 97\r\n"
	}
	if err := os.WriteFile(filepath.Join(stubDir, dockerName), []byte(dockerStub), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "scripts", "production-gates", "build-and-scan-images.ps1")
	commandArgs := append([]string{"-NoProfile", "-File", script}, args...)
	command := exec.Command("pwsh", commandArgs...)
	env := os.Environ()
	pathSet := false
	for i, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.EqualFold(name, "PATH") {
			continue
		}
		if value == "" {
			env[i] = name + "=" + stubDir
		} else {
			env[i] = name + "=" + stubDir + string(os.PathListSeparator) + value
		}
		pathSet = true
		break
	}
	if !pathSet {
		env = append(env, "PATH="+stubDir)
	}
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("credential-free publication plan invoked Docker: %v\n%s", err, output)
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
	case "delete-bypass":
		delete(rule, "bypass_actors")
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

func expectedPublicationRefs(registry, repository, version, commit string) map[string]string {
	ids := []struct {
		repository string
		id         string
	}{
		{registry + "/" + repository, "sha256:" + strings.Repeat("1", 64)},
		{registry + "/" + repository + "-operator-console", "sha256:" + strings.Repeat("2", 64)},
		{registry + "/" + repository + "-postgres", "sha256:" + strings.Repeat("3", 64)},
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

func assertDeferredPublicationPlan(t *testing.T, path string, expected map[string]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		RemoteInspection string `json:"remote_inspection"`
		Destinations     []struct {
			Reference    string `json:"reference"`
			ConfigDigest string `json:"config_digest"`
			Action       string `json:"action"`
		} `json:"destinations"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.RemoteInspection != "deferred-until-authenticated-publish" {
		t.Fatalf("credential-free plan remote inspection = %q, want deferred authenticated publish", plan.RemoteInspection)
	}
	seen := make(map[string]struct{}, len(plan.Destinations))
	for _, destination := range plan.Destinations {
		want, ok := expected[destination.Reference]
		if !ok {
			t.Fatalf("deferred publication plan leaked non-canonical alias %q", destination.Reference)
		}
		if _, duplicate := seen[destination.Reference]; duplicate {
			t.Fatalf("deferred publication plan repeated destination %q", destination.Reference)
		}
		seen[destination.Reference] = struct{}{}
		if destination.ConfigDigest != want {
			t.Fatalf("deferred publication plan changed exact image identity for %s", destination.Reference)
		}
		if destination.Action != "inspect-after-login" {
			t.Fatalf("deferred publication action for %s = %q, want inspect-after-login", destination.Reference, destination.Action)
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("deferred publication plan coverage mismatch: refs=%d, want %d", len(seen), len(expected))
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
	remoteRoot := filepath.Join(root, "github.com", "thebtf")
	if err := os.MkdirAll(remoteRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	remote := filepath.ToSlash(filepath.Join(remoteRoot, "engram.git"))
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
