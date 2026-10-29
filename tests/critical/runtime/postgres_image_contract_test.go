//go:build critical

package runtime_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// @critical
// @category: data-consistency
// @features: [image-remediation, postgres-persistence]
// @dev_stand: required
func TestPostgresImageContract(t *testing.T) {
	repo := repositoryRoot(t)
	dockerfile := filepath.Join(repo, "deploy", "postgres", "Dockerfile")
	requireFileContains(t, dockerfile,
		"docker.io/library/postgres:17.11-alpine3.24@sha256:e4cb93687b062cd25a0e8f672d85a7708235009d70d31f8a7698d9370092a9c4",
		"docker.io/library/golang:1.26.10-bookworm@sha256:5b1f78db1f54cc8cb46e652c51d3f854441c4310a449161b6fe9fb553edab505",
		"COPY --from=pgvector-build /out/usr/local/lib/postgresql/",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"USER 70:70",
	)
	requireFileNotContains(t, dockerfile, "apk.cgr.dev/chainguard")

	image := imageFromEnv("ENGRAM_POSTGRES_IMAGE", defaultPostgresImage)
	config := inspectImage(t, image)
	if config.Config.User != "70" && config.Config.User != "70:70" {
		t.Fatalf("PostgreSQL image must run as UID/GID 70, got %q", config.Config.User)
	}
	requireEnv(t, config.Config.Env, "LANG", "C.UTF-8")
	requireEnv(t, config.Config.Env, "LC_ALL", "C.UTF-8")
	requireProvenanceLabels(t, config.Config.Labels)
	requireHealthCommand(t, config.Config.Healthcheck, "pg_isready")

	t.Run("gosu retains identity switching", func(t *testing.T) {
		for _, flag := range []string{"-u", "-g"} {
			got := runDocker(t, nil, "run", "--rm", "--network", "none", "--read-only",
				"--user", "0:0", "--cap-drop", "ALL", "--cap-add", "SETUID", "--cap-add", "SETGID",
				"--entrypoint", "/usr/local/bin/gosu", image, "postgres", "id", flag)
			if strings.TrimSpace(string(got)) != "70" {
				t.Fatalf("gosu did not switch postgres identity (%s): %q", flag, got)
			}
		}
	})

	prefix := uniqueResource("engram-prc-postgres-test")
	network := "none"
	volume := prefix + "-data"
	first := prefix + "-first"
	second := prefix + "-second"
	legacyBlocked := prefix + "-legacy-owner-blocked"
	third := prefix + "-after-owner-migration"
	t.Cleanup(func() {
		removeContainer(first)
		removeContainer(second)
		removeContainer(legacyBlocked)
		removeContainer(third)
		removeVolume(volume)
	})
	runDocker(t, nil, "volume", "create", volume)
	startPostgresContainer(t, first, network, volume)
	waitHealthy(t, first, 90*time.Second)
	requireHardenedContainer(t, first, "70:70")
	requireVolumeMetadata(t, volume, image, "70:70:700")
	requirePostgresVersions(t, first)

	psql(t, first, "CREATE EXTENSION IF NOT EXISTS vector")
	psql(t, first, `CREATE TABLE image_contract_markers (id integer PRIMARY KEY, note text NOT NULL, embedding vector(3) NOT NULL)`)
	psql(t, first, `INSERT INTO image_contract_markers VALUES (1, 'persistent-image-contract', '[1,2,3]')`)
	if got := psql(t, first, `SELECT note || ':' || embedding::text FROM image_contract_markers WHERE id=1`); got != "persistent-image-contract:[1,2,3]" {
		t.Fatalf("unexpected marker before recreation: %q", got)
	}

	backup := runDocker(t, nil, "exec", first, "pg_dump", "-U", "engram", "-d", "engram", "--table=image_contract_markers", "--no-owner", "--no-privileges")
	psql(t, first, "CREATE DATABASE engram_restore")
	psqlDatabase(t, first, "engram_restore", "CREATE EXTENSION IF NOT EXISTS vector")
	runDocker(t, append(backup, '\n'), "exec", "-i", first, "psql", "-v", "ON_ERROR_STOP=1", "-U", "engram", "-d", "engram_restore")
	if got := psqlDatabase(t, first, "engram_restore", `SELECT note FROM image_contract_markers WHERE id=1`); got != "persistent-image-contract" {
		t.Fatalf("backup/restore marker mismatch: %q", got)
	}

	removeContainer(first)
	startPostgresContainer(t, second, network, volume)
	waitHealthy(t, second, 90*time.Second)
	requirePostgresVersions(t, second)
	if got := psql(t, second, `SELECT note || ':' || embedding::text FROM image_contract_markers WHERE id=1`); got != "persistent-image-contract:[1,2,3]" {
		t.Fatalf("persistent marker lost after container recreation: %q", got)
	}

	removeContainer(second)
	rewriteVolumeOwnership(t, volume, image, "999:999")
	requireVolumeMetadata(t, volume, image, "999:999:700")
	startPostgresContainer(t, legacyBlocked, network, volume)
	waitNotHealthy(t, legacyBlocked, 20*time.Second)
	removeContainer(legacyBlocked)
	migrateLegacyPostgresVolume(t, volume, image)
	requireVolumeMetadata(t, volume, image, "70:70:700")
	startPostgresContainer(t, third, network, volume)
	waitHealthy(t, third, 90*time.Second)
	if got := psql(t, third, `SELECT note || ':' || embedding::text FROM image_contract_markers WHERE id=1`); got != "persistent-image-contract:[1,2,3]" {
		t.Fatalf("persistent marker lost across legacy UID ownership migration: %q", got)
	}

	t.Run("invalid builtin locale never becomes ready", func(t *testing.T) {
		name := prefix + "-invalid-locale"
		t.Cleanup(func() { removeContainer(name) })
		runDocker(t, nil,
			"run", "-d", "--name", name,
			"--memory", "512m", "--cpus", "1", "--pids-limit", "128", "--network", "none",
			"--user", "70:70", "--read-only", "--cap-drop", "ALL",
			"--security-opt", "no-new-privileges:true",
			"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=64m",
			"--tmpfs", "/var/run/postgresql:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=16m",
			"--tmpfs", "/var/lib/postgresql/data:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=256m",
			"-e", "POSTGRES_INITDB_ARGS=--locale-provider=builtin --builtin-locale=invalid_locale_713",
			"-e", "POSTGRES_DB=engram", "-e", "POSTGRES_USER=engram", "-e", "POSTGRES_PASSWORD=engram",
			image,
		)
		waitNotHealthy(t, name, 45*time.Second)
		logs := string(runDocker(t, nil, "logs", name))
		if !strings.Contains(logs, `invalid locale name "invalid_locale_713" for builtin provider`) {
			t.Fatalf("invalid locale failed for an unexpected reason: %s", logs)
		}
	})

	t.Run("tmpfs-only data is not durable", func(t *testing.T) {
		name := prefix + "-tmpfs"
		t.Cleanup(func() { removeContainer(name) })
		startPostgresTmpfs(t, name, image)
		waitHealthy(t, name, 90*time.Second)
		psql(t, name, "CREATE TABLE volatile_marker (id integer PRIMARY KEY)")
		psql(t, name, "INSERT INTO volatile_marker VALUES (1)")
		removeContainer(name)
		startPostgresTmpfs(t, name, image)
		waitHealthy(t, name, 90*time.Second)
		cmd := execDocker("exec", name, "psql", "-v", "ON_ERROR_STOP=1", "-U", "engram", "-d", "engram", "-Atc", "SELECT COUNT(*) FROM volatile_marker")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("tmpfs-only PGDATA unexpectedly retained marker: %q", out)
		}
		if !strings.Contains(string(out), `relation "volatile_marker" does not exist`) {
			t.Fatalf("tmpfs-only PGDATA failed for an unexpected reason: %q (err: %v)", out, err)
		}
	})
}

func startPostgresContainer(t *testing.T, name, network, volume string) {
	t.Helper()
	image := imageFromEnv("ENGRAM_POSTGRES_IMAGE", defaultPostgresImage)
	args := []string{"run", "-d", "--name", name, "--network", network}
	if network != "none" {
		args = append(args, "--network-alias", "postgres")
	}
	args = append(args,
		"--memory", "512m", "--cpus", "1", "--pids-limit", "128",
		"--user", "70:70", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=64m",
		"--tmpfs", "/var/run/postgresql:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=16m",
		"-v", volume+":/var/lib/postgresql/data",
		"-e", "POSTGRES_DB=engram", "-e", "POSTGRES_USER=engram", "-e", "POSTGRES_PASSWORD=engram",
		image,
	)
	runDocker(t, nil, args...)
}

func startPostgresTmpfs(t *testing.T, name, image string) {
	t.Helper()
	runDocker(t, nil,
		"run", "-d", "--name", name,
		"--memory", "512m", "--cpus", "1", "--pids-limit", "128", "--network", "none",
		"--user", "70:70", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=64m",
		"--tmpfs", "/var/run/postgresql:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=16m",
		"--tmpfs", "/var/lib/postgresql/data:rw,noexec,nosuid,nodev,uid=70,gid=70,mode=0700,size=256m",
		"-e", "POSTGRES_DB=engram", "-e", "POSTGRES_USER=engram", "-e", "POSTGRES_PASSWORD=engram",
		image,
	)
}

func requirePostgresVersions(t *testing.T, container string) {
	t.Helper()
	if got := psql(t, container, "SHOW server_version_num"); got != "170011" {
		t.Fatalf("PostgreSQL version mismatch: %q", got)
	}
	psql(t, container, "CREATE EXTENSION IF NOT EXISTS vector")
	if got := psql(t, container, "SELECT extversion FROM pg_extension WHERE extname='vector'"); got != "0.8.1" {
		t.Fatalf("pgvector version mismatch: %q", got)
	}
}

func rewriteVolumeOwnership(t *testing.T, volume, image, owner string) {
	t.Helper()
	runDocker(t, nil,
		"run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh",
		"--memory", "64m", "--cpus", "0.2", "--pids-limit", "32", "--network", "none",
		"-v", volume+":/data", image,
		"-c", "chown -R "+owner+" /data && chmod 0700 /data",
	)
}

func migrateLegacyPostgresVolume(t *testing.T, volume, image string) {
	t.Helper()
	runDocker(t, nil,
		"run", "--rm", "--user", "0:0",
		"--memory", "64m", "--cpus", "0.2", "--pids-limit", "32", "--network", "none",
		"--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER",
		"--security-opt", "no-new-privileges:true", "--entrypoint", "/bin/sh",
		"-v", volume+":/var/lib/postgresql/data", image,
		"-c", "chown -R 70:70 /var/lib/postgresql/data && chmod 0700 /var/lib/postgresql/data",
	)
}

func psql(t *testing.T, container, sql string) string {
	t.Helper()
	return psqlDatabase(t, container, "engram", sql)
}

func psqlDatabase(t *testing.T, container, database, sql string) string {
	t.Helper()
	out := runDocker(t, nil, "exec", container, "psql", "-v", "ON_ERROR_STOP=1", "-U", "engram", "-d", database, "-Atc", sql)
	return strings.TrimSpace(string(out))
}

func execDocker(args ...string) *exec.Cmd {
	return exec.Command("docker", args...)
}
