import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const scriptRoot = dirname(scriptPath);
const defaultRepository = resolve(scriptRoot, "../..");
const commandTimeoutMs = 2 * 60 * 1000;
const coverageTestTimeoutMs = 30 * 60 * 1000;
const dockerStartupTimeoutMs = 2 * 60 * 1000;
const dockerReadyTimeoutMs = 60 * 1000;
const databaseUser = "engram_sonar";
const databasePassword = "engram_sonar_disposable";
const installedUCIDatabaseEnv = "ENGRAM_UCI_INSTALLED_TEST_DATABASE_DSN";
const hapFixtureDatabaseEnv = "HAP01C_FIXTURE_TEST_DSN";
const coverageProfiles = [
  { name: "base", target: "./...", race: true },
  {
    name: "cross-package",
    target: "./...",
    coverpkg: "./cmd/...,./internal/...,./pkg/...",
  },
  { name: "uci", target: "./internal/db/gorm", run: "^TestUCI", databasePrefix: "sonar_uci" },
  {
    name: "migrations",
    target: "./internal/db/gorm",
    run: "^TestMigration|^TestMigrations",
    databasePrefix: "sonar_migrations",
  },
  {
    name: "behavioral-rules",
    target: "./internal/db/gorm",
    run: "^TestBehavioralRules",
    databasePrefix: "sonar_behavioral_rules",
  },
  {
    name: "browser-context",
    target: "./internal/db/gorm",
    run: "^(TestBrowser(ReadGrant|TabBinding)|TestCollectionSelection|TestBrowserCodeContextStore_)",
    databasePrefix: "sonar_browser",
  },
  {
    name: "intervention",
    target: "./internal/db/gorm",
    run: "^TestIntervention(Policy|Receipt)",
    databasePrefix: "sonar_intervention",
  },
  {
    name: "candidate",
    target: "./internal/db/gorm",
    run: "^(TestCandidate|TestOpenCandidate)",
    databasePrefix: "sonar_candidate",
  },
  {
    name: "governance",
    target: "./internal/db/gorm",
    run: "^(TestRuleGovernanceStore_|TestRuleInjectionEventStore_|TestMigration1[45][0-9])",
    skip: "^TestRuleGovernanceStore_GetLifecycleHealth",
    databasePrefix: "sonar_governance",
  },
  {
    name: "memory-batch-token",
    target: "./internal/db/gorm",
    run: "^(TestMemoryStore_|TestBatch|TestToken|TestTokenize)",
    skip: "^TestMemoryStore_QueryMetaIndex_FTSVisibilityStopsAtScanBudget$",
    databasePrefix: "sonar_memory",
  },
  {
    name: "session-store",
    target: "./internal/db/gorm",
    run: "^TestSessionStore",
    databasePrefix: "sonar_session",
  },
  {
    name: "project-identity",
    target: "./internal/db/gorm",
    run: "^(TestProjectIdentity|TestRegisterAndResolve|TestValidateProjectIdentity|TestUpsertProject|TestAttachLegacyAlias|TestObserveLegacyOutcome)",
    databasePrefix: "sonar_project_identity",
  },
  {
    name: "code-chunk",
    target: "./internal/db/gorm",
    run: "^(TestCodeChunkStore_|TestMigration139_)",
    databasePrefix: "sonar_code_chunk",
  },
  {
    name: "issues",
    target: "./internal/db/gorm",
    run: "^(TestIssueStore|TestCloseIssue|TestAcknowledge)",
    skip: "^TestIssueStoreApplySelectionOperationPostgres$",
    databasePrefix: "sonar_issues",
  },
  { name: "purge", target: "./internal/db/gorm", run: "^TestPurge", databasePrefix: "sonar_purge" },
  {
    name: "task-memory-candidates",
    target: "./internal/db/gorm",
    run: "^TestTaskMemoryCandidateStore_",
    databasePrefix: "sonar_task_memory",
  },
  {
    name: "worker",
    target: "./internal/worker",
    skip: "^(TestAuthHandlersLifecycle_LastAdminDemoteRaceLeavesOneAdmin|TestAuthHandlersLifecycle_LastAdminDemoteDisableRaceLeavesOneAdmin|TestAuthHandlersLifecycle_DisabledAdminCanBeDemotedWithoutLastAdminError|TestTokenAuth_AuthentikProvisioningRequiresInitialAdminSetup)$",
    databasePrefix: "sonar_worker",
  },
  {
    name: "uci-installed",
    target: "./cmd/engram",
    run: "^TestUCIInstalledStandardClientsKeepDirtyViewsIsolated$",
    databasePrefix: "sonar_uci_test",
    databaseEnvironment: installedUCIDatabaseEnv,
  },
  {
    name: "hap-fixture",
    target: "./internal/hap01cfixture",
    run: "^TestFixtureSeedRotateAndSnapshotIntegration$",
    databasePrefix: "hap01c",
    databaseEnvironment: hapFixtureDatabaseEnv,
  },
  {
    name: "operator-code-fixture",
    target: "./cmd/operator-code-live-fixture",
    databasePrefix: "operator_code",
  },
];

let statusPublication = null;

export function parseOptions(argv) {
  const options = {
    publishStatus: false,
    qualityGateTimeout: 600,
    repository: defaultRepository,
    scannerCommand: "sonar-scanner",
  };

  for (let index = 0; index < argv.length; index += 1) {
    const option = argv[index];
    if (option === "--publish-status") {
      options.publishStatus = true;
      continue;
    }

    const value = argv[index + 1];
    if (!value) throw new Error(`Missing value for ${option}`);
    if (option === "--repository") options.repository = resolve(value);
    else if (option === "--scanner") options.scannerCommand = value;
    else if (option === "--timeout") {
      if (!/^[1-9]\d*$/.test(value)) throw new Error("--timeout must be a positive integer");
      options.qualityGateTimeout = Number(value);
      if (!Number.isSafeInteger(options.qualityGateTimeout)) {
        throw new Error("--timeout exceeds the safe integer range");
      }
    } else {
      throw new Error(`Unknown option: ${option}`);
    }
    index += 1;
  }
  return options;
}

function commandInvocation(command, args) {
  const isWindowsBatch = process.platform === "win32" && /\.(?:bat|cmd)$/i.test(command);
  return {
    executable: isWindowsBatch ? process.env.ComSpec || "cmd.exe" : command,
    args: isWindowsBatch ? ["/d", "/c", command, ...args] : args,
  };
}

function commandError(command, result) {
  if (result.error?.code === "ETIMEDOUT") return new Error(`${command} timed out`);
  if (result.error) return new Error(`${command} failed: ${result.error.message}`);
  if (result.status === null) return new Error(`${command} ended without an exit code`);
  return new Error(`${command} failed with exit code ${result.status}`);
}

function capture(command, args, cwd, { env = process.env, timeoutMs = commandTimeoutMs } = {}) {
  const invocation = commandInvocation(command, args);
  const result = spawnSync(invocation.executable, invocation.args, {
    cwd,
    encoding: "utf8",
    env,
    timeout: timeoutMs,
    windowsHide: true,
  });
  if (result.error || result.status !== 0) throw commandError(command, result);
  return result.stdout.trim();
}

function run(command, args, cwd, { env = process.env, timeoutMs = commandTimeoutMs } = {}) {
  const invocation = commandInvocation(command, args);
  const result = spawnSync(invocation.executable, invocation.args, {
    cwd,
    env,
    stdio: "inherit",
    timeout: timeoutMs,
    windowsHide: true,
  });
  if (result.error || result.status !== 0) throw commandError(command, result);
}

function locate(command) {
  if (existsSync(command)) return resolve(command);
  const locator = process.platform === "win32" ? "where.exe" : "which";
  const result = spawnSync(locator, [command], {
    encoding: "utf8",
    timeout: commandTimeoutMs,
    windowsHide: true,
  });
  if (!result.error && result.status === 0) {
    return result.stdout.split(/\r?\n/, 1)[0].trim();
  }
  return null;
}

function parseReport(path) {
  if (!existsSync(path)) throw new Error(`SonarScanner did not write ${path}`);
  const values = new Map();
  for (const line of readFileSync(path, "utf8").split(/\r?\n/)) {
    if (!line.trim()) continue;
    const separator = line.indexOf("=");
    if (separator < 1) throw new Error(`Malformed report-task line: ${line}`);
    const key = line.slice(0, separator).trim();
    if (!key || values.has(key)) throw new Error(`Malformed report-task key: ${line}`);
    values.set(key, line.slice(separator + 1).trim());
  }
  for (const key of ["ceTaskUrl", "dashboardUrl"]) {
    if (!values.get(key)) throw new Error(`report-task.txt is missing ${key}`);
  }
  return values;
}

function reportTarget(path) {
  try {
    const report = parseReport(path);
    return report.get("dashboardUrl") || report.get("ceTaskUrl") || null;
  } catch {
    return null;
  }
}

function readSonarDotEnv(path) {
  const values = new Map();
  if (!existsSync(path)) return values;
  for (const rawLine of readFileSync(path, "utf8").split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const match = /^(?:export\s+)?(SONAR_TOKEN|SONAR_HOST_URL)\s*=\s*(.*)$/.exec(line);
    if (!match) continue;
    if (values.has(match[1])) throw new Error(`Duplicate ${match[1]} in ${path}`);
    let value = match[2].trim();
    if (
      value.length >= 2 &&
      ((value.startsWith('"') && value.endsWith('"')) ||
        (value.startsWith("'") && value.endsWith("'")))
    ) {
      value = value.slice(1, -1);
    }
    values.set(match[1], value);
  }
  return values;
}

function scrubTestEnvironment() {
  const environment = { ...process.env };
  delete environment.DATABASE_DSN;
  delete environment[installedUCIDatabaseEnv];
  delete environment[hapFixtureDatabaseEnv];
  delete environment.SONAR_TOKEN;
  return environment;
}

function sleep(milliseconds) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, milliseconds);
}

export function parseDockerPort(output) {
  for (const line of output.trim().split(/\r?\n/)) {
    const match = /:(\d+)\s*$/.exec(line);
    if (!match) continue;
    const port = Number(match[1]);
    if (Number.isInteger(port) && port > 0 && port <= 65535) return port;
  }
  throw new Error(`Docker did not report a valid PostgreSQL host port: ${output.trim()}`);
}

function waitForPostgres(dockerCommand, containerName, cwd) {
  const deadline = Date.now() + dockerReadyTimeoutMs;
  while (Date.now() < deadline) {
    try {
      run(
        dockerCommand,
        ["exec", containerName, "pg_isready", "--username", databaseUser, "--dbname", "postgres"],
        cwd,
        { timeoutMs: 5 * 1000 },
      );
      return;
    } catch {
      sleep(1000);
    }
  }
  throw new Error("Timed out waiting for the runner-owned PostgreSQL container");
}

function stopContainer(dockerCommand, containerName, cwd) {
  run(dockerCommand, ["rm", "--force", "--volumes", containerName], cwd, {
    timeoutMs: commandTimeoutMs,
  });
}

function startPostgres(dockerCommand, cwd) {
  const runID = `q${process.pid}_${randomUUID().replaceAll("-", "").slice(0, 12)}`;
  const containerName = `engram-sonarqube-${runID}`;
  let started = false;
  try {
    run(
      dockerCommand,
      [
        "run",
        "--detach",
        "--name",
        containerName,
        "--publish",
        "127.0.0.1::5432",
        "--env",
        `POSTGRES_USER=${databaseUser}`,
        "--env",
        `POSTGRES_PASSWORD=${databasePassword}`,
        "--env",
        "POSTGRES_DB=postgres",
        "pgvector/pgvector:pg17",
      ],
      cwd,
      { timeoutMs: dockerStartupTimeoutMs },
    );
    started = true;
    waitForPostgres(dockerCommand, containerName, cwd);
    const port = parseDockerPort(
      capture(dockerCommand, ["port", containerName, "5432/tcp"], cwd, {
        timeoutMs: commandTimeoutMs,
      }),
    );
    let databaseNumber = 0;
    return {
      createDatabase(prefix) {
        databaseNumber += 1;
        const name = `${prefix}_${runID}_${databaseNumber}`;
        if (!/^[a-z][a-z0-9_]{0,62}$/.test(name)) {
          throw new Error(`Invalid runner database name: ${name}`);
        }
        run(dockerCommand, ["exec", containerName, "createdb", "--username", databaseUser, name], cwd, {
          timeoutMs: commandTimeoutMs,
        });
        run(
          dockerCommand,
          [
            "exec",
            containerName,
            "psql",
            "--username",
            databaseUser,
            "--dbname",
            name,
            "--command",
            "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;",
          ],
          cwd,
          { timeoutMs: commandTimeoutMs },
        );
        return `postgres://${databaseUser}:${databasePassword}@127.0.0.1:${port}/${name}?sslmode=disable`;
      },
      stop() {
        stopContainer(dockerCommand, containerName, cwd);
      },
    };
  } catch (error) {
    if (started) {
      try {
        stopContainer(dockerCommand, containerName, cwd);
      } catch (cleanupError) {
        console.error(`Could not remove runner-owned PostgreSQL container: ${cleanupError.message}`);
      }
    }
    throw error;
  }
}

function profileEnvironment(baseEnvironment, profile, databaseDSN) {
  if (!databaseDSN) return baseEnvironment;
  const environment = { ...baseEnvironment };
  if (profile.databaseEnvironment === installedUCIDatabaseEnv) {
    environment[installedUCIDatabaseEnv] = databaseDSN;
  } else if (profile.databaseEnvironment === hapFixtureDatabaseEnv) {
    environment[hapFixtureDatabaseEnv] = databaseDSN;
  } else {
    environment.DATABASE_DSN = databaseDSN;
  }
  return environment;
}

function runCoverageProfile(goCommand, profile, coveragePath, cwd, environment, databaseDSN) {
  const args = [
    "test",
    "-count=1",
    ...(databaseDSN ? ["-parallel=1"] : []),
    profile.target,
    "-covermode=atomic",
    `-coverprofile=${relative(cwd, coveragePath)}`,
  ];
  if (profile.race) args.push("-race");
  if (profile.coverpkg) args.push(`-coverpkg=${profile.coverpkg}`);
  if (profile.run) args.push(`-run=${profile.run}`);
  if (profile.skip) args.push(`-skip=${profile.skip}`);
  run(goCommand, args, cwd, {
    env: profileEnvironment(environment, profile, databaseDSN),
    timeoutMs: coverageTestTimeoutMs,
  });
}

function compareCoverageBlocks([left], [right]) {
  if (left < right) return -1;
  if (left > right) return 1;
  return 0;
}

export function mergeCoverProfiles(profilePaths, destination) {
  let mode = null;
  const blocks = new Map();
  for (const profilePath of profilePaths) {
    if (!existsSync(profilePath)) throw new Error(`Go test did not write ${profilePath}`);
    const lines = readFileSync(profilePath, "utf8").split(/\r?\n/);
    const header = /^mode:\s*(\S+)\s*$/.exec(lines[0] || "");
    if (!header) throw new Error(`Malformed coverprofile header: ${profilePath}`);
    if (mode && mode !== header[1]) {
      throw new Error(`Coverage mode mismatch: ${mode} and ${header[1]}`);
    }
    mode = header[1];
    for (const line of lines.slice(1)) {
      if (!line.trim()) continue;
      const block = /^(.*\s+\d+)\s+(\d+)$/.exec(line);
      if (!block) throw new Error(`Malformed coverprofile block in ${profilePath}: ${line}`);
      const hitCount = Number(block[2]);
      if (!Number.isSafeInteger(hitCount) || hitCount < 0) {
        throw new Error(`Invalid coverprofile hit count in ${profilePath}: ${line}`);
      }
      const previous = blocks.get(block[1]);
      if (previous === undefined || hitCount > previous) blocks.set(block[1], hitCount);
    }
  }
  if (mode !== "atomic") throw new Error(`Coverage mode must be atomic, got ${mode || "none"}`);
  if (blocks.size === 0) throw new Error("No Go coverage blocks were collected");
  writeFileSync(
    destination,
    `mode: ${mode}\n${[...blocks.entries()]
      .sort(compareCoverageBlocks)
      .map(([block, hitCount]) => `${block} ${hitCount}`)
      .join("\n")}\n`,
    "utf8",
  );
}

function collectCoverage(goCommand, dockerCommand, cwd, coverageDirectory, outputPath, environment) {
  const profilePaths = [];
  let postgres = null;
  let primaryError = null;
  try {
    for (const profile of coverageProfiles) {
      if (profile.databasePrefix && !postgres) postgres = startPostgres(dockerCommand, cwd);
      const databaseDSN = profile.databasePrefix ? postgres.createDatabase(profile.databasePrefix) : null;
      const coveragePath = join(coverageDirectory, `${profile.name}.out`);
      runCoverageProfile(goCommand, profile, coveragePath, cwd, environment, databaseDSN);
      profilePaths.push(coveragePath);
    }
    mergeCoverProfiles(profilePaths, outputPath);
  } catch (error) {
    primaryError = error;
    throw error;
  } finally {
    if (postgres) {
      try {
        postgres.stop();
      } catch (cleanupError) {
        if (primaryError) {
          console.error(`Could not remove runner-owned PostgreSQL container: ${cleanupError.message}`);
        } else {
          throw cleanupError;
        }
      }
    }
  }
}

function ensureDocker(dockerCommand, cwd) {
  try {
    capture(dockerCommand, ["version", "--format", "{{.Server.Version}}"], cwd);
  } catch (error) {
    throw new Error(`Docker is required for isolated coverage: ${error.message}`);
  }
}

function createStatusPublication(repoRoot, head) {
  const ghCommand = locate("gh");
  if (!ghCommand) throw new Error("GitHub CLI is required for --publish-status");
  try {
    capture(ghCommand, ["auth", "status"], repoRoot);
  } catch (error) {
    throw new Error(`An authenticated GitHub CLI session is required for --publish-status: ${error.message}`);
  }
  const repository = capture(
    ghCommand,
    ["repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"],
    repoRoot,
  );
  if (!/^[^/\s]+\/[^/\s]+$/.test(repository)) {
    throw new Error(`GitHub CLI returned an invalid owner/repository: ${repository}`);
  }
  return { ghCommand, head, repository, repoRoot, targetUrl: null };
}

function publishCommitStatus(publication, state) {
  const args = [
    "api",
    "--method",
    "POST",
    `repos/${publication.repository}/statuses/${publication.head}`,
    "--raw-field",
    `state=${state}`,
    "--raw-field",
    "context=SonarQube Quality Gate",
    "--raw-field",
    `description=${state === "pending" ? "SonarQube Quality Gate is running" : state === "success" ? "SonarQube Quality Gate passed" : "SonarQube Quality Gate failed"}`,
  ];
  if (publication.targetUrl) args.push("--raw-field", `target_url=${publication.targetUrl}`);
  run(publication.ghCommand, args, publication.repoRoot);
}

function assertExactHeadAndCleanTree(repoRoot, head) {
  const finalHead = capture("git", ["rev-parse", "--verify", "HEAD^{commit}"], repoRoot).toLowerCase();
  if (finalHead !== head) throw new Error(`Git HEAD changed during analysis: ${head} -> ${finalHead}`);
  if (capture("git", ["status", "--porcelain"], repoRoot)) {
    throw new Error("Working tree changed during SonarQube analysis");
  }
}

export function main(options = parseOptions(process.argv.slice(2))) {
  const repoRoot = resolve(capture("git", ["rev-parse", "--show-toplevel"], options.repository));
  if (!existsSync(join(repoRoot, "sonar-project.properties"))) {
    throw new Error(`Repository has no sonar-project.properties: ${repoRoot}`);
  }
  const head = capture("git", ["rev-parse", "--verify", "HEAD^{commit}"], repoRoot).toLowerCase();
  if (!/^[0-9a-f]{40}$/.test(head)) throw new Error(`Invalid Git HEAD: ${head}`);

  const commonGitDir = resolve(
    capture("git", ["rev-parse", "--path-format=absolute", "--git-common-dir"], repoRoot),
  );
  const coordinationRoot = dirname(commonGitDir);
  const receiptDirectory = join(coordinationRoot, ".agent", "e", "sonarqube");
  const receiptPath = join(receiptDirectory, `${head}.json`);
  const scannerDirectory = join(repoRoot, ".scannerwork");
  const reportTaskPath = join(scannerDirectory, "report-task.txt");
  const temporaryCoverageDirectory = join(scannerDirectory, "coverage");
  const coverageOutputPath = join(repoRoot, "coverage.out");
  mkdirSync(receiptDirectory, { recursive: true });
  rmSync(receiptPath, { force: true });
  rmSync(reportTaskPath, { force: true });
  rmSync(temporaryCoverageDirectory, { force: true, recursive: true });
  rmSync(coverageOutputPath, { force: true });

  if (capture("git", ["status", "--porcelain"], repoRoot)) {
    throw new Error("Refusing to run SonarQube against a dirty working tree");
  }
  if (options.publishStatus) {
    statusPublication = createStatusPublication(repoRoot, head);
    publishCommitStatus(statusPublication, "pending");
  }

  const worktreeDotEnv = readSonarDotEnv(join(repoRoot, ".env"));
  const sharedDotEnv =
    coordinationRoot === repoRoot ? new Map() : readSonarDotEnv(join(coordinationRoot, ".env"));
  const sonarToken =
    process.env.SONAR_TOKEN?.trim() ||
    worktreeDotEnv.get("SONAR_TOKEN")?.trim() ||
    sharedDotEnv.get("SONAR_TOKEN")?.trim();
  if (!sonarToken) throw new Error("SONAR_TOKEN is required in the process environment or project .env");
  const sonarHostUrl =
    process.env.SONAR_HOST_URL?.trim() ||
    worktreeDotEnv.get("SONAR_HOST_URL")?.trim() ||
    sharedDotEnv.get("SONAR_HOST_URL")?.trim();
  if (!sonarHostUrl) {
    throw new Error("SONAR_HOST_URL is required in the process environment or project .env");
  }
  const parsedHost = new URL(sonarHostUrl);
  if (!['http:', 'https:'].includes(parsedHost.protocol)) {
    throw new Error("SONAR_HOST_URL must use http or https");
  }
  if (
    parsedHost.username ||
    parsedHost.password ||
    parsedHost.search ||
    parsedHost.hash ||
    (parsedHost.pathname !== "/" && parsedHost.pathname !== "")
  ) {
    throw new Error("SONAR_HOST_URL must be a credential-free server base URL");
  }

  const goCommand = locate("go");
  if (!goCommand) throw new Error("Go is required to generate coverage");
  const dockerCommand = locate("docker");
  if (!dockerCommand) throw new Error("Docker is required to generate isolated coverage");
  let scannerPath = locate(options.scannerCommand);
  if (!scannerPath && process.platform === "win32" && options.scannerCommand === "sonar-scanner") {
    const localAppData = process.env.LOCALAPPDATA;
    if (localAppData) {
      const perUserScanner = join(
        localAppData,
        "SonarScanner",
        "8.1.0.6389",
        "sonar-scanner-8.1.0.6389-windows-x64",
        "bin",
        "sonar-scanner.bat",
      );
      if (existsSync(perUserScanner)) scannerPath = perUserScanner;
    }
  }
  if (!scannerPath) throw new Error(`SonarScanner was not found: ${options.scannerCommand}`);
  ensureDocker(dockerCommand, repoRoot);


  const testEnvironment = scrubTestEnvironment();
  const scannerEnvironment = { ...testEnvironment, SONAR_TOKEN: sonarToken };
  let complete = false;
  try {
    mkdirSync(temporaryCoverageDirectory, { recursive: true });
    collectCoverage(
      goCommand,
      dockerCommand,
      repoRoot,
      temporaryCoverageDirectory,
      coverageOutputPath,
      testEnvironment,
    );
    run(
      scannerPath,
      [
        `-Dsonar.host.url=${sonarHostUrl}`,
        `-Dsonar.scm.revision=${head}`,
        `-Dsonar.buildString=${head}`,
        "-Dsonar.qualitygate.wait=true",
        `-Dsonar.qualitygate.timeout=${options.qualityGateTimeout}`,
      ],
      repoRoot,
      { env: scannerEnvironment, timeoutMs: (options.qualityGateTimeout + 300) * 1000 },
    );

    assertExactHeadAndCleanTree(repoRoot, head);
    const report = parseReport(reportTaskPath);
    if (statusPublication) {
      statusPublication.targetUrl = report.get("dashboardUrl") || report.get("ceTaskUrl") || null;
    }
    const receipt = {
      schema_version: 1,
      gate: "sonarqube",
      project_key: "thebtf_engram",
      sonar_host_url: sonarHostUrl,
      verdict: "PASS",
      head,
      completed_at_utc: new Date().toISOString(),
      ce_task_url: report.get("ceTaskUrl"),
      dashboard_url: report.get("dashboardUrl"),
    };
    const receiptJson = `${JSON.stringify(receipt, null, 2)}\n`;
    if (receiptJson.includes(sonarToken) || /postgres(?:ql)?:\/\//i.test(receiptJson)) {
      throw new Error("Receipt contains secret connection data");
    }
    const temporaryReceipt = join(receiptDirectory, `.${head}.${randomUUID()}.tmp`);
    try {
      writeFileSync(temporaryReceipt, receiptJson, { encoding: "utf8", mode: 0o600 });
      renameSync(temporaryReceipt, receiptPath);
    } finally {
      rmSync(temporaryReceipt, { force: true });
    }
    if (statusPublication) publishCommitStatus(statusPublication, "success");
    complete = true;
    console.log(receiptPath);
  } finally {
    rmSync(temporaryCoverageDirectory, { force: true, recursive: true });
    if (!complete) rmSync(coverageOutputPath, { force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  try {
    main();
  } catch (error) {
    if (statusPublication) {
      statusPublication.targetUrl ||= reportTarget(
        join(statusPublication.repoRoot, ".scannerwork", "report-task.txt"),
      );
      try {
        publishCommitStatus(statusPublication, "failure");
      } catch (statusError) {
        console.error(`Failed to publish GitHub failure status: ${statusError.message}`);
      }
    }
    console.error(error.stack || error.message);
    process.exitCode = 1;
  }
}
