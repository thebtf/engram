import { spawn, spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  appendFileSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  readlinkSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { arch, hostname, platform } from "node:os";
import { basename, dirname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const scriptRoot = dirname(scriptPath);
const defaultRepository = resolve(scriptRoot, "../..");
const databaseUser = "engram_sonar";
const databasePassword = "engram_sonar_disposable";
const imageReference = "pgvector/pgvector:pg17";
const installedUCIDatabaseEnv = "ENGRAM_UCI_INSTALLED_TEST_DATABASE_DSN";
const hapFixtureDatabaseEnv = "HAP01C_FIXTURE_TEST_DSN";
const projectKey = "thebtf_engram";
const schemaVersion = 2;
const commandTimeoutSeconds = 120;
const dockerReadyTimeoutSeconds = 60;
const cleanupReserveSeconds = 120;
const safeDurationLimitSeconds = 24 * 60 * 60;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;


export const coverageProfiles = Object.freeze([
  {
    name: "base",
    unitized: true,
    resourceGroup: "exclusive",
    packageConcurrency: 1,
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
    run: "^(TestBrowser(ReadGrant|TabBinding)|TestCollectionSelection|TestBrowserCodeContextStore)",
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
  { name: "session-store", target: "./internal/db/gorm", run: "^TestSessionStore", databasePrefix: "sonar_session" },
  {
    name: "project-identity",
    target: "./internal/db/gorm",
    run: "^(TestProjectIdentity|TestRegisterAndResolve|TestValidateProjectIdentity|TestUpsertProject|TestAttachLegacyAlias|TestObserveLegacyOutcome)",
    databasePrefix: "sonar_project_identity",
  },
  { name: "code-chunk", target: "./internal/db/gorm", run: "^(TestCodeChunkStore_|TestMigration139_)", databasePrefix: "sonar_code_chunk" },
  {
    name: "issues",
    target: "./internal/db/gorm",
    run: "^(TestIssueStore|TestCloseIssue|TestAcknowledge)",
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
    name: "worker-auth",
    target: "./internal/worker",
    run: "^(TestAuthHandlersLifecycle|TestAuthHandlersSetup|TestAuthTokenHandlers|TestHandleCreateToken)",
    skip: "^(TestAuthHandlersLifecycle_LastAdminDemoteRaceLeavesOneAdmin|TestAuthHandlersLifecycle_LastAdminDemoteDisableRaceLeavesOneAdmin|TestAuthHandlersLifecycle_DisabledAdminCanBeDemotedWithoutLastAdminError)$",
    databasePrefix: "sonar_worker_auth",
  },
  { name: "worker-uci", target: "./internal/worker", run: "^TestUCIApplication", databasePrefix: "sonar_worker_uci" },
  {
    name: "worker-memory",
    target: "./internal/worker",
    run: "^(TestHandleStoreMemoryExplicit|TestApplyPrincipalMemoryMetadataREST|TestHandleListMemories|TestHandleDeleteMemory|TestHandleGetMemoryAudit|TestHandleSuppressMemory|TestHandleGetMemoryByID|TestMemoryCollectionSelection|TestMemoryDomainsHandlers|TestMemoryDomainManageAllowedREST)",
    databasePrefix: "sonar_worker_memory",
  },
  {
    name: "worker-issues",
    target: "./internal/worker",
    run: "^(TestIssueHTTP|TestIssueSelection)",
    databasePrefix: "sonar_worker_issues",
  },
  {
    name: "worker-context",
    target: "./internal/worker",
    run: "^Test(ContextInject_(IdentityOnlyRegistersSynchronouslyAndIdempotently|LegacyMetadataPreservesOuterCanonical|LegacyAliasWithInternalWhitespaceRemainsCompatible|LegacyMetadataDoesNotClaimForeignAlias|FailsClosedWithoutIdentityStore|AmbiguousLegacyFailsWithUpgradeActionBeforeAccess|RejectsRawSelectorAndMetadataBeforeProjectMutation|UnknownSelectorOnlyFailsBeforeProjectMutation)|ProjectIdentityHTTPError_(DoesNotExposeDatabaseDiagnostics|TypedNilFailsClosed))$",
    databasePrefix: "sonar_worker_context",
  },
  {
    name: "worker-admin-race",
    target: "./internal/worker",
    run: "^TestAuthHandlersLifecycle_LastAdminDemoteRaceLeavesOneAdmin$",
    databasePrefix: "sonar_worker_admin_race",
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
    databasePrefix: "hap01c_s",
    databaseEnvironment: hapFixtureDatabaseEnv,
    resourceGroup: "isolated-fixture",
  },
  {
    name: "operator-code-fixture",
    target: "./cmd/operator-code-live-fixture",
    databasePrefix: "operator_code",
    resourceGroup: "isolated-fixture",
  },
].map((profile) => Object.freeze({ resourceGroup: "exclusive", packageConcurrency: 1, ...profile })));

class RunnerError extends Error {
  constructor(message, exitCode = 1, retryable = false) {
    super(message);
    this.name = "RunnerError";
    this.exitCode = exitCode;
    this.retryable = retryable;
  }
}

class BudgetError extends RunnerError {
  constructor(message) {
    super(message, 124);
    this.name = "BudgetError";
  }
}

class SignalError extends RunnerError {
  constructor(signal) {
    super(`${signal} received; runner-owned processes were cancelled`, signal === "SIGINT" ? 130 : 143);
    this.name = "SignalError";
  }
}

export function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function canonicalize(value) {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonicalize(value[key])]));
  }
  return value;
}

export function canonicalJson(value) {
  return JSON.stringify(canonicalize(value));
}

function shaFile(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function atomicWrite(path, contents, mode = 0o600) {
  mkdirSync(dirname(path), { recursive: true });
  const temporary = join(dirname(path), `.${basename(path)}.${randomUUID()}.tmp`);
  try {
    writeFileSync(temporary, contents, { encoding: "utf8", mode });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

function writeJson(path, value) {
  atomicWrite(path, `${JSON.stringify(value, null, 2)}\n`);
}

function commandInvocation(command, args) {
  const isWindowsBatch = process.platform === "win32" && /\.(?:bat|cmd)$/i.test(command);
  return {
    executable: isWindowsBatch ? process.env.ComSpec || "cmd.exe" : command,
    args: isWindowsBatch ? ["/d", "/c", command, ...args] : args,
  };
}

function commandFailure(command, result) {
  if (result.error) return new RunnerError(`${command} failed: ${result.error.message}`);
  if (result.signal) return new RunnerError(`${command} was terminated by ${result.signal}`);
  return new RunnerError(`${command} failed with exit code ${result.code}`);
}

function redacted(value, secrets) {
  let result = String(value);
  for (const secret of secrets) {
    if (secret && secret.length >= 3) result = result.split(secret).join("[REDACTED]");
  }
  return result.replace(/postgres(?:ql)?:\/\/[^\s"']+/gi, "[REDACTED_DSN]");
}

function safeRedactionBoundary(value, boundary, secrets) {
  for (const secret of secrets) {
    if (!secret || secret.length < 3) continue;
    for (let length = Math.min(secret.length - 1, boundary); length > 0; length -= 1) {
      if (value.slice(boundary - length, boundary) === secret.slice(0, length)) boundary -= length;
    }
  }
  const dsn = Math.max(value.lastIndexOf("postgres://", boundary), value.lastIndexOf("postgresql://", boundary));
  return dsn >= 0 && dsn >= boundary - 1024 ? dsn : boundary;
}
function redactionTailLength(secrets) {
  return Math.max("postgresql://".length, ...secrets.map((secret) => (secret?.length || 0) + 1));
}

export function redactChunks(chunks, secrets) {
  let tail = "";
  let output = "";
  const tailLength = redactionTailLength(secrets);
  for (const chunk of chunks) {
    const value = tail + String(chunk);
    const boundary = safeRedactionBoundary(value, Math.max(0, value.length - tailLength), secrets);
    output += redacted(value.slice(0, boundary), secrets);
    tail = value.slice(boundary);
  }
  return output + redacted(tail, secrets);
}
export function createLogWriter(path, secrets) {
  let tail = "";
  const tailLength = redactionTailLength(secrets);
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, "", { encoding: "utf8", mode: 0o600 });
  return {
    write(chunk) {
      const value = tail + String(chunk);
      const boundary = safeRedactionBoundary(value, Math.max(0, value.length - tailLength), secrets);
      if (boundary) appendFileSync(path, redacted(value.slice(0, boundary), secrets), { encoding: "utf8", mode: 0o600 });
      tail = value.slice(boundary);
    },
    finish() {
      if (tail) appendFileSync(path, redacted(tail, secrets), { encoding: "utf8", mode: 0o600 });
      tail = "";
    },
  };
}

async function waitForOwnedExit(record, timeoutMs = 5000) {
  if (record.child.exitCode !== null || record.child.signalCode) return;
  const exited = await new Promise((resolveExit) => {
    const timeout = setTimeout(() => resolveExit(false), timeoutMs);
    record.child.once("close", () => {
      clearTimeout(timeout);
      resolveExit(true);
    });
  });
  if (!exited && record.child.exitCode === null && !record.child.signalCode) {
    throw new RunnerError(`Runner-owned child did not exit: ${record.label || record.command}`);
  }
}

async function terminateOwnedChild(record) {
  if (record.child.exitCode !== null || record.child.signalCode) return;
  if (process.platform === "win32") {
    await new Promise((resolveTermination, rejectTermination) => {
      const killer = spawn("taskkill.exe", ["/pid", String(record.child.pid), "/t", "/f"], { stdio: "ignore", windowsHide: true });
      killer.once("error", rejectTermination);
      killer.once("close", (code) => code === 0 ? resolveTermination() : rejectTermination(new RunnerError(`taskkill failed for owned PID ${record.child.pid}`)));
    });
    await waitForOwnedExit(record);
    return;
  }
  try { process.kill(-record.child.pid, "SIGTERM"); } catch { }
  try {
    await waitForOwnedExit(record, 2000);
  } catch {
    try { process.kill(-record.child.pid, "SIGKILL"); } catch { }
    await waitForOwnedExit(record);
  }
}

async function terminateOwnedChildren(children) {
  await Promise.all([...children.values()].map(terminateOwnedChild));
}

export function cleanupExecution(execution) {
  return { ...execution, signal: new AbortController().signal };
}

function runProcess(command, args, {
  cwd,
  env = process.env,
  timeoutMs,
  signal,
  children,
  label,
  onStdout,
  onStderr,
  onStart,
} = {}) {
  const invocation = commandInvocation(command, args);
  return new Promise((resolveProcess, rejectProcess) => {
    let settled = false;
    let timeout = null;
    let abortListener = null;
    let stdout = "";
    let stderr = "";
    let terminationCause = null;
    const child = spawn(invocation.executable, invocation.args, {
      cwd,
      env,
      detached: process.platform !== "win32",
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
    const record = { child, label, command, started_at_utc: new Date().toISOString() };
    children?.set(child.pid, record);
    children?.history?.push({ pid: child.pid, label, started_at_utc: record.started_at_utc });
    onStart?.(record);
    const finish = (callback, value, retainChild = false) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      if (signal && abortListener) signal.removeEventListener("abort", abortListener);
      if (!retainChild) children?.delete(child.pid);
      callback(value);
    };
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
      onStdout?.(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
      onStderr?.(chunk);
    });
    child.once("error", (error) => finish(rejectProcess, terminationCause || new RunnerError(`${command} failed: ${error.message}`)));
    child.once("close", (code, childSignal) => {
      if (terminationCause) {
        finish(rejectProcess, terminationCause);
        return;
      }
      const result = { code, signal: childSignal, stdout, stderr };
      if (code === 0) finish(resolveProcess, result);
      else finish(rejectProcess, commandFailure(command, result));
    });
    if (timeoutMs) {
      timeout = setTimeout(async () => {
        if (terminationCause) return;
        terminationCause = new BudgetError(`${label || command} exceeded its budget`);
        try {
          await terminateOwnedChild(record);
          finish(rejectProcess, terminationCause);
        } catch (error) {
          finish(rejectProcess, error, true);
        }
      }, timeoutMs);
    }
    if (signal) {
      abortListener = async () => {
        if (terminationCause) return;
        terminationCause = signal.reason || new SignalError("SIGTERM");
        try {
          await terminateOwnedChild(record);
          finish(rejectProcess, terminationCause);
        } catch (error) {
          finish(rejectProcess, error, true);
        }
      };
      if (signal.aborted) abortListener();
      else signal.addEventListener("abort", abortListener, { once: true });
    }
  });
}

export async function runOwnedCommand(command, args, options = {}) {
  const children = new Map();
  children.history = [];
  try {
    return await runProcess(command, args, { ...options, children });
  } finally {
    await terminateOwnedChildren(children);
  }
}

async function capture(command, args, cwd, options = {}) {
  const timeoutMs = options.timeoutMs ?? (options.deadline ? options.deadline.commandTimeout(commandTimeoutSeconds * 1000) : commandTimeoutSeconds * 1000);
  const result = await runProcess(command, args, { ...options, cwd, timeoutMs });
  return result.stdout.trim();
}

async function bestEffortCapture(command, args, cwd, options = {}) {
  try {
    return await capture(command, args, cwd, options);
  } catch {
    return "unavailable";
  }
}

function locate(command) {
  if (existsSync(command)) return resolve(command);
  const extensions = process.platform === "win32" ? (process.env.PATHEXT || ".EXE;.CMD;.BAT").split(";") : [""];
  for (const directory of (process.env.PATH || "").split(process.platform === "win32" ? ";" : ":")) {
    if (!directory) continue;
    for (const extension of extensions) {
      const path = join(directory, process.platform === "win32" ? command.endsWith(extension.toLowerCase()) ? command : `${command}${extension}` : command);
      if (existsSync(path)) return path;
    }
  }
  return null;
}

function resolveScanner(command) {
  const located = locate(command);
  if (located) return located;
  if (process.platform === "win32" && command === "sonar-scanner" && process.env.LOCALAPPDATA) {
    const fallback = join(process.env.LOCALAPPDATA, "SonarScanner", "8.1.0.6389", "sonar-scanner-8.1.0.6389-windows-x64", "bin", "sonar-scanner.bat");
    if (existsSync(fallback)) return fallback;
  }
  return null;
}

function validDuration(option, value) {
  if (!/^[1-9]\d*$/.test(value)) throw new RunnerError(`${option} must be a positive integer`);
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed > safeDurationLimitSeconds) {
    throw new RunnerError(`${option} exceeds the safe duration limit`);
  }
  return parsed;
}

export function parseOptions(argv) {
  const options = {
    mode: "gate",
    run: null,
    fresh: false,
    jobs: 2,
    publishStatus: false,
    baseOnly: false,
    repository: defaultRepository,
    scannerCommand: "sonar-scanner",
    qualityGateTimeout: 600,
    overallTimeout: 4500,
    coverageTimeout: 3600,
    profileTimeout: 1800,
    scannerTimeout: 300,
  };
  const seen = new Set();
  for (let index = 0; index < argv.length; index += 1) {
    const option = argv[index];
    if (!option.startsWith("--")) throw new RunnerError(`Unknown option: ${option}`);
    if (seen.has(option)) throw new RunnerError(`Duplicate option: ${option}`);
    seen.add(option);
    if (option === "--publish-status") {
      options.publishStatus = true;
      continue;
    }
    if (option === "--fresh") {
      options.fresh = true;
      continue;
    }
    if (option === "--base-only") {
      options.baseOnly = true;
      continue;
    }
    const value = argv[index + 1];
    if (!value || value.startsWith("--")) throw new RunnerError(`Missing value for ${option}`);
    if (option === "--repository") options.repository = resolve(value);
    else if (option === "--scanner") options.scannerCommand = value;
    else if (option === "--mode") {
      if (!new Set(["gate", "coverage", "scan", "resume"]).has(value)) throw new RunnerError("--mode must be gate, coverage, scan, or resume");
      options.mode = value;
    } else if (option === "--run") options.run = value;
    else if (option === "--jobs") {
      if (value !== "1" && value !== "2") throw new RunnerError("--jobs must be 1 or 2");
      options.jobs = Number(value);
    } else if (option === "--timeout") options.qualityGateTimeout = validDuration(option, value);
    else if (option === "--overall-timeout") options.overallTimeout = validDuration(option, value);
    else if (option === "--coverage-timeout") options.coverageTimeout = validDuration(option, value);
    else if (option === "--profile-timeout") options.profileTimeout = validDuration(option, value);
    else if (option === "--scanner-timeout") options.scannerTimeout = validDuration(option, value);
    else throw new RunnerError(`Unknown option: ${option}`);
    index += 1;
  }
  if (options.mode === "resume" && (!options.run || !uuidPattern.test(options.run))) throw new RunnerError("--mode resume requires --run <UUID>");
  if (options.mode !== "resume" && options.run) throw new RunnerError("--run is only valid with --mode resume");
  if (options.mode !== "gate" && options.fresh && options.mode !== "coverage") throw new RunnerError("--fresh is only valid with gate or coverage mode");
  if (options.baseOnly && options.mode !== "coverage") throw new RunnerError("--base-only is only valid in coverage mode");
  if (options.mode === "coverage" && options.publishStatus) throw new RunnerError("--publish-status is invalid in coverage mode");
  return options;
}

export function parseDockerPort(output) {
  for (const line of output.trim().split(/\r?\n/)) {
    const match = /:(\d+)\s*$/.exec(line);
    if (!match) continue;
    const port = Number(match[1]);
    if (Number.isInteger(port) && port > 0 && port <= 65535) return port;
  }
  throw new RunnerError(`Docker did not report a valid PostgreSQL host port: ${output.trim()}`);
}

function parseAtomicCoverage(contents, source) {
  const value = String(contents);
  const lines = value.split(/\r?\n/);
  if (/\r?\n$/.test(value)) lines.pop();
  if (!/^mode:\s*\S+\s*$/.test(lines[0] || "")) throw new RunnerError(`Malformed coverprofile header: ${source}`);
  if (lines[0] !== "mode: atomic") throw new RunnerError(`Coverage mode must be atomic in ${source}`);
  const blocks = new Map();
  for (const line of lines.slice(1)) {
    const block = /^(.+):(\d+)\.(\d+),(\d+)\.(\d+)\s+(\d+)\s+(\d+)$/.exec(line);
    if (!block) throw new RunnerError(`Malformed coverprofile block in ${source}: ${line}`);
    const [startLine, startColumn, endLine, endColumn, statements, hits] = block.slice(2).map(Number);
    if (![startLine, startColumn, endLine, endColumn, statements, hits].every(Number.isSafeInteger) ||
      startLine < 1 || startColumn < 0 || endLine < startLine || endColumn < 0 ||
      (endLine === startLine && endColumn < startColumn) || hits < 0) {
      throw new RunnerError(`Invalid coverprofile block in ${source}: ${line}`);
    }
    const range = `${block[1]}:${block[2]}.${block[3]},${block[4]}.${block[5]}`;
    const previous = blocks.get(range);
    if (previous && previous.statements !== statements) throw new RunnerError(`Conflicting coverprofile statement count in ${source}: ${line}`);
    blocks.set(range, { statements, hits: Math.max(previous?.hits || 0, hits) });
  }
  return blocks;
}
function hasCoverableStatements(blocks) {
  for (const block of blocks.values()) if (block.statements > 0) return true;
  return false;
}

export function normalizeCoverage(reports) {
  if (!reports.length) throw new RunnerError("Coverage mode must be atomic, got none");
  const blocks = new Map();
  for (const report of reports) {
    const source = report.source || "coverage";
    for (const [range, block] of parseAtomicCoverage(report.contents, source)) {
      const previous = blocks.get(range);
      if (previous && previous.statements !== block.statements) throw new RunnerError(`Conflicting coverprofile statement count in ${source}: ${range}`);
      blocks.set(range, { statements: block.statements, hits: Math.max(previous?.hits || 0, block.hits) });
    }
  }
  if (!hasCoverableStatements(blocks)) throw new RunnerError("No Go coverage blocks were collected");
  return `mode: atomic\n${[...blocks.entries()].sort(([left], [right]) => left.localeCompare(right)).map(([range, block]) => `${range} ${block.statements} ${block.hits}`).join("\n")}\n`;
}

export function mergeCoverProfiles(profilePaths, destination) {
  const reports = profilePaths.map((profilePath) => {
    if (!existsSync(profilePath)) throw new RunnerError(`Go test did not write ${profilePath}`);
    return { source: profilePath, contents: readFileSync(profilePath, "utf8") };
  });
  atomicWrite(destination, normalizeCoverage(reports));
}

function validateCoverage(path, expectedDigest = null, { allowZeroCoverableStatements = false } = {}) {
  if (!existsSync(path) || statSync(path).size === 0) throw new RunnerError(`Coverage artifact is missing or empty: ${path}`);
  const digest = shaFile(path);
  if (expectedDigest && digest !== expectedDigest) throw new RunnerError(`Coverage digest mismatch: ${path}`);
  const blocks = parseAtomicCoverage(readFileSync(path, "utf8"), path);
  const coverage_classification = hasCoverableStatements(blocks) ? "covered" : "zero_coverable_statements";
  if (coverage_classification === "zero_coverable_statements" && !allowZeroCoverableStatements) throw new RunnerError(`Coverage artifact has no coverable statements: ${path}`);
  return { sha256: digest, bytes: statSync(path).size, coverage_classification };
}

function safeRelative(root, value) {
  const rootPath = realpathSync(root);
  const lexical = resolve(rootPath, value);
  if (lexical !== rootPath && !lexical.startsWith(`${rootPath}${sep}`)) throw new RunnerError(`Artifact path escapes its campaign: ${value}`);
  const path = realpathSync(lexical);
  if (path !== rootPath && !path.startsWith(`${rootPath}${sep}`)) throw new RunnerError(`Artifact path resolves outside its campaign: ${value}`);
  return path;
}
function digestPath(path) {
  const info = lstatSync(path);
  if (info.isSymbolicLink()) return digestPath(realpathSync(path));
  if (info.isFile()) return shaFile(path);
  if (info.isDirectory()) return sha256(canonicalJson(inventoryEntries(path).sort((left, right) => left.path.localeCompare(right.path))));
  return "unsupported";
}

function inventoryEntries(root, directory = root, entries = [], deadline = null) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    deadline?.commandTimeout(1);
    const path = join(directory, entry.name);
    const rel = relative(root, path).replaceAll("\\", "/");
    if (!rel) continue;
    if (rel === ".git" || rel.startsWith(".git/") || rel === ".agent" || rel.startsWith(".agent/")) continue;
    if (rel === ".scannerwork" || rel.startsWith(".scannerwork/") || rel === "coverage.out" || rel === ".env") continue;
    const info = lstatSync(path);
    if (info.isSymbolicLink()) {
      const resolved = realpathSync(path);
      entries.push({ path: rel, type: "symlink", target: readlinkSync(path), resolved_sha256: digestPath(resolved) });
    } else if (info.isDirectory()) {
      entries.push({ path: rel, type: "directory" });
      inventoryEntries(root, path, entries, deadline);
    } else if (info.isFile()) {
      entries.push({ path: rel, type: "file", sha256: shaFile(path), bytes: info.size });
    }
  }
  return entries;
}

export function sourceInventory(root, deadline = null) {
  const entries = inventoryEntries(root, root, [], deadline).sort((left, right) => left.path.localeCompare(right.path));
  return { entries, sha256: sha256(canonicalJson(entries)) };
}

async function sourceInventoryAsync(root, deadline) {
  const entries = [];
  let visited = 0;
  const walk = async (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      deadline.commandTimeout(1);
      visited += 1;
      if (visited % 64 === 0) await new Promise((resolveYield) => setImmediate(resolveYield));
      const path = join(directory, entry.name);
      const rel = relative(root, path).replaceAll("\\", "/");
      if (!rel || rel === ".git" || rel.startsWith(".git/") || rel === ".agent" || rel.startsWith(".agent/") || rel === ".scannerwork" || rel.startsWith(".scannerwork/") || rel === "coverage.out" || rel === ".env") continue;
      const info = lstatSync(path);
      if (info.isSymbolicLink()) {
        const resolved = realpathSync(path);
        entries.push({ path: rel, type: "symlink", target: readlinkSync(path), resolved_sha256: digestPath(resolved) });
      } else if (info.isDirectory()) {
        entries.push({ path: rel, type: "directory" });
        await walk(path);
      } else if (info.isFile()) {
        entries.push({ path: rel, type: "file", sha256: shaFile(path), bytes: info.size });
      }
    }
  };
  await walk(root);
  entries.sort((left, right) => left.path.localeCompare(right.path));
  return { entries, sha256: sha256(canonicalJson(entries)) };
}

function scrubTestEnvironment() {
  const environment = { ...process.env };
  delete environment.DATABASE_DSN;
  delete environment[installedUCIDatabaseEnv];
  delete environment[hapFixtureDatabaseEnv];
  delete environment.SONAR_TOKEN;
  delete environment.SONAR_HOST_URL;
  return environment;
}

function localWorkspaceInputs(root, environment) {
  const inputs = [];
  for (const config of [join(root, "go.mod"), join(root, "go.work")]) {
    if (!existsSync(config)) continue;
    inputs.push({ name: relative(root, config), sha256: shaFile(config) });
    for (const line of readFileSync(config, "utf8").split(/\r?\n/)) {
      const match = /(?:=>|^\s*use\s+)(\.?\.?[\\/][^\s)]+|[A-Za-z]:[\\/][^\s)]+)/.exec(line);
      if (!match) continue;
      const input = resolve(dirname(config), match[1]);
      if (existsSync(input)) inputs.push({ name: relative(root, input).replaceAll("\\", "/"), sha256: digestPath(realpathSync(input)) });
    }
  }
  for (const name of ["ENGRAM_UCI_PARSER_EXECUTABLE", "CC", "CXX", "GOMOD", "GOWORK"]) {
    const value = environment[name];
    if (typeof value !== "string" || !value || !existsSync(value)) continue;
    inputs.push({ name, sha256: digestPath(realpathSync(value)) });
  }
  return inputs.sort((left, right) => canonicalJson(left).localeCompare(canonicalJson(right)));
}

async function resolveImage(dockerCommand, cwd, execution) {
  let image = await bestEffortCapture(dockerCommand, ["image", "inspect", "--format", "{{.Id}}", imageReference], cwd, execution);
  if (!/^sha256:[0-9a-f]{64}$/i.test(image)) {
    await capture(dockerCommand, ["pull", imageReference], cwd, execution);
    image = await capture(dockerCommand, ["image", "inspect", "--format", "{{.Id}}", imageReference], cwd, execution);
  }
  if (!/^sha256:[0-9a-f]{64}$/i.test(image)) throw new RunnerError("Docker did not return an immutable pgvector image ID");
  return image.toLowerCase();
}

async function coverageEnvironment(repoRoot, { goCommand, dockerCommand, imageId, execution }) {
  const testEnvironment = scrubTestEnvironment();
  const values = {
    node: process.version,
    platform: platform(),
    architecture: arch(),
    git: await bestEffortCapture("git", ["--version"], repoRoot, execution),
    go: goCommand ? await bestEffortCapture(goCommand, ["version"], repoRoot, execution) : "unavailable",
    go_env: goCommand ? await bestEffortCapture(goCommand, ["env", "GOOS", "GOARCH", "GOAMD64", "GOVERSION", "GOTOOLCHAIN", "CGO_ENABLED", "CC", "CXX", "GOFLAGS", "GOEXPERIMENT", "GOMOD", "GOWORK", "GOROOT"], repoRoot, execution) : "unavailable",
    cc: await bestEffortCapture(testEnvironment.CC || "cc", ["--version"], repoRoot, execution),
    cxx: await bestEffortCapture(testEnvironment.CXX || "c++", ["--version"], repoRoot, execution),
    image_id: imageId,
    file_inputs: localWorkspaceInputs(repoRoot, testEnvironment),
    test_environment_sha256: sha256(canonicalJson(testEnvironment)),
  };
  return { values, sha256: sha256(canonicalJson(values)), testEnvironment };
}
function profilePackageConcurrency(profile) {
  if (![1, 2].includes(profile.packageConcurrency)) throw new RunnerError(`${profile.name} has invalid package concurrency`);
  return profile.packageConcurrency;
}

const serializedPackageSuffixes = Object.freeze([
  "/internal/db/gorm",
  "/internal/hap01cfixture",
  "/internal/hostadvisor",
  "/internal/worker",
  "/cmd/engram",
  "/cmd/operator-code-live-fixture",
]);

export function classifyPackageUnit(packageInfo) {
  const packagePath = typeof packageInfo === "string" ? packageInfo : packageInfo.importPath;
  return packageInfo.serial || serializedPackageSuffixes.some((suffix) => packagePath.endsWith(suffix)) ? "serial" : "ordinary";
}

export function boundedCoverpkg(packagePath, directImports, firstPartyPackages) {
  const available = new Set(firstPartyPackages);
  return [packagePath, ...directImports.filter((item) => item !== packagePath && available.has(item))]
    .filter((item, index, values) => values.indexOf(item) === index)
    .sort();
}

export function planPackageUnits(packages, { coreOnly = false } = {}) {
  const firstPartyPackages = packages.map((item) => item.importPath);
  const units = [];
  for (const packageInfo of packages) {
    if (!packageInfo.importPath || !(packageInfo.testGoFiles?.length || packageInfo.xTestGoFiles?.length)) continue;
    const classification = classifyPackageUnit(packageInfo);
    if (coreOnly && classification !== "ordinary") continue;
    for (const phase of ["race", "coverage"]) {
      const coverpkg = phase === "coverage" ? boundedCoverpkg(packageInfo.importPath, packageInfo.imports || [], firstPartyPackages) : [];
      units.push(Object.freeze({
        id: `base-${phase}-${sha256(packageInfo.importPath).slice(0, 16)}`,
        phase,
        importPath: packageInfo.importPath,
        directory: packageInfo.directory,
        classification,
        core: classification === "ordinary",
        coverpkg,
      }));
    }
  }
  return units.sort((left, right) => (left.phase === right.phase ? left.importPath.localeCompare(right.importPath) : left.phase === "race" ? -1 : 1));
}

function parseGoList(output) {
  const values = [];
  let start = null;
  let depth = 0;
  let quoted = false;
  let escaped = false;
  for (let index = 0; index < output.length; index += 1) {
    const character = output[index];
    if (quoted) {
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === '"') quoted = false;
      continue;
    }
    if (character === '"') { quoted = true; continue; }
    if (character === "{") {
      if (depth === 0) start = index;
      depth += 1;
      continue;
    }
    if (character !== "}") continue;
    depth -= 1;
    if (depth === 0 && start !== null) {
      values.push(JSON.parse(output.slice(start, index + 1)));
      start = null;
    }
  }
  if (depth !== 0 || quoted) throw new RunnerError("go list returned malformed package metadata");
  return values;
}

async function discoverPackageUnits(goCommand, cwd, execution, deadline, profileDeadline, coreOnly) {
  const result = await runProcess(goCommand, ["list", "-json", "./..."], {
    ...execution,
    cwd,
    timeoutMs: profileTimeout(deadline, profileDeadline, commandTimeoutSeconds * 1000),
    label: "base package discovery",
  });
  const packages = parseGoList(result.stdout)
    .filter((item) => item.ImportPath && item.Module?.Path && item.ImportPath.startsWith(item.Module.Path))
    .map((item) => ({ importPath: item.ImportPath, directory: relative(cwd, item.Dir).replaceAll("\\", "/"), imports: item.Imports || [], testGoFiles: item.TestGoFiles || [], xTestGoFiles: item.XTestGoFiles || [] }));
  return planPackageUnits(packages, { coreOnly });
}

export function profileDescriptor(profile) {
  if (profile.unitized) return { ...profile, effective_argv: [] };
  const args = ["test", "-json", `-p=${profilePackageConcurrency(profile)}`, "-count=1", profile.target];
  if (profile.unitPhase !== "race") args.push("-covermode=atomic");
  if (profile.databasePrefix) args.push("-parallel=1");
  if (profile.race) args.push("-race");
  if (profile.coverpkg) args.push(`-coverpkg=${profile.coverpkg}`);
  if (profile.run) args.push(`-run=${profile.run}`);
  if (profile.skip) args.push(`-skip=${profile.skip}`);
  return { ...profile, effective_argv: args };
}

export function testInventoryArguments(profile) {
  if (profile.unitized) return [];
  return ["test", `-p=${profilePackageConcurrency(profile)}`, "-list", ".", profile.target];
}

function testInventoryFingerprint(profile, candidate) {
  if (profile.unitized) return sha256(canonicalJson([]));
  const prefix = profile.target === "./..." ? "" : `${profile.target.slice(2)}/`;
  return sha256(canonicalJson((candidate.inventory || []).filter((entry) => entry.type === "file" && entry.path.endsWith("_test.go") && (!prefix || entry.path.startsWith(prefix)))));
}

export function fingerprintProfile(profile, candidate, environment) {
  return sha256(canonicalJson({
    descriptor: profileDescriptor(profile),
    candidate_inputs_sha256: candidate.inputs_sha256,
    test_inventory_sha256: testInventoryFingerprint(profile, candidate),
    environment_sha256: environment.sha256,
    runner_sha256: shaFile(scriptPath),
  }));
}

async function candidateFor(repoRoot, execution) {
  const root = realpathSync(repoRoot);
  const head = (await capture("git", ["rev-parse", "--verify", "HEAD^{commit}"], root, execution)).toLowerCase();
  const tree = (await capture("git", ["rev-parse", "--verify", "HEAD^{tree}"], root, execution)).toLowerCase();
  const commonGitDir = realpathSync(await capture("git", ["rev-parse", "--path-format=absolute", "--git-common-dir"], root, execution));
  if (!/^[0-9a-f]{40}$/.test(head) || !/^[0-9a-f]{40}$/.test(tree)) throw new RunnerError("Git did not return a full commit/tree identity");
  const inventory = await sourceInventoryAsync(root, execution.deadline);
  execution.deadline.commandTimeout(1);
  return {
    repository_path: root,
    repository_id: sha256(commonGitDir),
    worktree_id: sha256(root),
    head,
    tree,
    inputs_sha256: inventory.sha256,
    inventory: inventory.entries,
    coordination_root: dirname(commonGitDir),
  };
}

async function assertCandidate(candidate, execution, { requireClean = false } = {}) {
  const current = await candidateFor(candidate.repository_path, execution);
  for (const key of ["repository_id", "worktree_id", "head", "tree", "inputs_sha256"]) {
    if (current[key] !== candidate[key]) throw new RunnerError(`Candidate changed while running (${key})`);
  }
  if (requireClean && await capture("git", ["status", "--porcelain"], candidate.repository_path, execution)) {
    throw new RunnerError("Refusing to run SonarQube against a dirty working tree");
  }
}

function readJson(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
}

function manifestsIn(namespace) {
  const runs = join(namespace, "runs");
  if (!existsSync(runs)) return [];
  return readdirSync(runs, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && uuidPattern.test(entry.name))
    .map((entry) => ({ runDir: join(runs, entry.name), manifest: readJson(join(runs, entry.name, "manifest.json")) }))
    .filter(({ manifest }) => manifest?.schema_version === schemaVersion && uuidPattern.test(manifest.run_id))
    .sort(({ manifest: left }, { manifest: right }) => String(right.started_at_utc).localeCompare(String(left.started_at_utc)));
}

function referencedRun(record, runId, candidate, environment) {
  if (!uuidPattern.test(runId)) return null;
  try {
    const runsRoot = realpathSync(dirname(record.runDir));
    const runDir = realpathSync(join(runsRoot, runId));
    if (dirname(runDir) !== runsRoot) return null;
    const manifest = readJson(join(runDir, "manifest.json"));
    if (!manifest || manifest.schema_version !== schemaVersion || manifest.run_id !== runId || !sameCandidate(manifest.candidate, candidate) || manifest.fingerprints?.coverage_environment !== environment.sha256) return null;
    return { runDir, manifest };
  } catch {
    return null;
  }
}

function sameCandidate(left, right) {
  return left && right && ["repository_id", "worktree_id", "head", "tree", "inputs_sha256"].every((key) => left[key] === right[key]);
}

function artifactFrom(manifestRecord, artifact, { allowZeroCoverableStatements = false } = {}) {
  if (!artifact?.path || !artifact.sha256) throw new RunnerError("Profile artifact metadata is incomplete");
  const path = safeRelative(manifestRecord.runDir, artifact.path);
  const verified = validateCoverage(path, artifact.sha256, { allowZeroCoverableStatements });
  if (artifact.coverage_classification && artifact.coverage_classification !== verified.coverage_classification) throw new RunnerError("Coverage artifact classification mismatch");
  if (artifact.bytes !== verified.bytes) throw new RunnerError("Profile artifact byte count mismatch");
  return { path, ...verified };
}

function retainedEvidence(record, entry, candidate, environment) {
  const visited = new Set();
  let sourceRecord = record;
  let sourceEntry = entry;
  while (sourceEntry?.source_run_id) {
    if (visited.has(sourceEntry.source_run_id)) return null;
    visited.add(sourceEntry.source_run_id);
    sourceRecord = referencedRun(sourceRecord, sourceEntry.source_run_id, candidate, environment);
    sourceEntry = sourceRecord?.manifest?.profiles?.find((profile) => profile.name === entry.name);
    if (!sourceRecord || !sourceEntry) return null;
  }
  return sourceRecord && sourceEntry ? { record: sourceRecord, entry: sourceEntry } : null;
}

function rejectedTestEvidence(localArtifact, reason) {
  return {
    execution: "rejected",
    local_artifact: localArtifact ? "passed" : "rejected",
    campaign_obligations: "rejected",
    allowed_skip_obligations: [],
    passed_tests: new Set(),
    passed_packages: new Set(),
    package_outputs: new Map(),
    reason,
  };
}

function ownerHasPassedRequiredTest(record, obligation, candidate, environment) {
  const ownerProfile = coverageProfiles.find((profile) => profile.name === obligation.required_profile);
  const ownerEntry = record.manifest?.profiles?.find((profile) => profile.name === obligation.required_profile);
  if (!ownerProfile || !ownerEntry) return false;
  const source = retainedEvidence(record, ownerEntry, candidate, environment);
  if (!source || source.entry.status !== "passed" || source.entry.fingerprint !== fingerprintProfile(ownerProfile, candidate, environment)) return false;
  const evidence = evaluateTestEvidence(source.record, source.entry, ownerProfile, candidate, environment, { checkCampaignObligations: false });
  const requiredTest = obligation.test.split("/")[0];
  if (evidence.execution !== "passed" || evidence.local_artifact !== "passed" ||
    !source.entry.expected_tests.some((test) => test.package === obligation.package && test.test === requiredTest) ||
    !evidence.passed_tests.has(testIdentity(obligation.package, requiredTest)) ||
    !evidence.passed_packages.has(obligation.package)) return false;
  try {
    artifactFrom(source.record, source.entry.coverage);
    return true;
  } catch {
    return false;
  }
}

function evaluateTestEvidence(manifestRecord, entry, profile, candidate, environment, { checkCampaignObligations = true } = {}) {
  if (!Array.isArray(entry.expected_tests) || !entry.expected_tests.length || entry.expected_tests.some((item) => !item?.package || !item?.test)) return rejectedTestEvidence(false, "expected tests are incomplete");
  if (entry.expected_test_inventory_sha256 !== sha256(canonicalJson(entry.expected_tests))) return rejectedTestEvidence(false, "expected test inventory digest mismatched");
  if (!entry.test_events?.path || !entry.test_events.sha256 || !Number.isSafeInteger(entry.test_events.bytes)) return rejectedTestEvidence(false, "test event metadata is incomplete");
  let path;
  try {
    path = safeRelative(manifestRecord.runDir, entry.test_events.path);
    if (!existsSync(path) || statSync(path).size !== entry.test_events.bytes || shaFile(path) !== entry.test_events.sha256) return rejectedTestEvidence(false, "test event artifact mismatched");
  } catch {
    return rejectedTestEvidence(false, "test event artifact is unavailable");
  }
  const passedTests = new Set();
  const passedPackages = new Set();
  const packageOutputs = new Map();
  const skipped = [];
  const reasons = new Map();
  try {
    for (const line of readFileSync(path, "utf8").split(/\r?\n/)) {
      if (!line.trim()) continue;
      const value = JSON.parse(line);
      const key = value.Test ? testIdentity(value.Package || "", value.Test) : null;
      if (value.Action === "fail") return rejectedTestEvidence(true, "Go test events reported failure");
      if (key && value.Action === "output") reasons.set(key, `${reasons.get(key) || ""}${value.Output || ""}`);
      if (value.Package && !value.Test && value.Action === "output") packageOutputs.set(value.Package, `${packageOutputs.get(value.Package) || ""}${value.Output || ""}`);
      if (key && value.Action === "pass") passedTests.add(key);
      if (value.Package && !value.Test && value.Action === "pass") passedPackages.add(value.Package);
      if (key && value.Action === "skip") skipped.push({ package: value.Package || "", test: value.Test, reason: reasons.get(key)?.trim() || "" });
    }
  } catch {
    return rejectedTestEvidence(true, "test event artifact is malformed");
  }
  const obligations = skipped.map((skip) => skipAdmission(profile, skip, candidate));
  if (obligations.some((obligation) => !obligation)) return rejectedTestEvidence(true, "Go test events reported an unapproved skip");
  const acceptedTests = new Set([...passedTests, ...obligations.map((obligation) => testIdentity(obligation.package, obligation.test))]);
  const counts = entry.package_counts || {};
  if (!passedPackages.size || !Number.isSafeInteger(counts.passed) || !Number.isSafeInteger(counts.failed) ||
    !Number.isSafeInteger(counts.tests_passed) || !Number.isSafeInteger(counts.tests_skipped) ||
    counts.passed !== passedPackages.size || counts.failed !== 0 || counts.tests_passed !== passedTests.size || counts.tests_skipped !== skipped.length ||
    !entry.expected_tests.every((test) => acceptedTests.has(testIdentity(test.package, test.test))) || entry.unexpected_skip_count) {
    return rejectedTestEvidence(true, "test event evidence is incomplete");
  }
  const direct = obligations.filter((obligation) => obligation.kind !== "conditional");
  const campaignObligations = !checkCampaignObligations || direct.every((obligation) => ownerHasPassedRequiredTest(manifestRecord, obligation, candidate, environment))
    ? "passed"
    : "deferred";
  return {
    execution: "passed",
    local_artifact: "passed",
    campaign_obligations: campaignObligations,
    allowed_skip_obligations: obligations,
    passed_tests: passedTests,
    passed_packages: passedPackages,
    package_outputs: packageOutputs,
    reason: null,
  };
}

function zeroCoverableEvidence(evidence, entry) {
  const expectedPackages = new Set(entry.expected_tests.map((test) => test.package));
  return expectedPackages.size > 0 && [...expectedPackages].every((packageName) =>
    evidence.passed_packages.has(packageName) &&
    String(evidence.package_outputs.get(packageName) || "").split(/\r?\n/).some((line) => line === "coverage: [no statements]"),
  );
}

export function reusableProfile(manifestRecord, profile, candidate, environment) {
  const manifest = manifestRecord?.manifest;
  if (!manifest || !sameCandidate(manifest.candidate, candidate)) return null;
  const entry = manifest.profiles?.find((item) => item.name === profile.name);
  if (!entry || entry.status !== "passed" || entry.fingerprint !== fingerprintProfile(profile, candidate, environment)) return null;
  const sourceRecord = entry.source_run_id ? referencedRun(manifestRecord, entry.source_run_id, candidate, environment) : manifestRecord;
  if (!sourceRecord) return null;
  const sourceEntry = sourceRecord.manifest?.profiles?.find((item) => item.name === profile.name);
  if (!sourceEntry || sourceEntry.status !== "passed" || sourceEntry.fingerprint !== fingerprintProfile(profile, candidate, environment)) return null;
  const evidence = evaluateTestEvidence(sourceRecord, sourceEntry, profile, candidate, environment);
  if (evidence.execution !== "passed" || evidence.local_artifact !== "passed" || evidence.campaign_obligations !== "passed") return null;
  try {
    return {
      entry,
      artifact: artifactFrom(sourceRecord, sourceEntry.coverage, { allowZeroCoverableStatements: zeroCoverableEvidence(evidence, sourceEntry) }),
      sourceRun: entry.source_run_id || manifest.run_id,
    };
  } catch {
    return null;
  }
}
function findReusableProfiles(namespace, profiles, candidate, environment) {
  const found = new Map();
  for (const record of manifestsIn(namespace)) {
    for (const profile of profiles) {
      if (!found.has(profile.name)) {
        const reusable = reusableProfile(record, profile, candidate, environment);
        if (reusable) found.set(profile.name, reusable);
      }
    }
  }
  return found;
}

function completeCoverage(record, candidate, environment) {
  const manifest = record.manifest;
  if (!sameCandidate(manifest.candidate, candidate) || manifest.fingerprints?.coverage_environment !== environment.sha256) return null;
  if (manifest.result?.coverage !== "passed" || manifest.merged?.status !== "passed") return null;
  if (!Array.isArray(manifest.profiles) || manifest.profiles.length !== coverageProfiles.length) return null;
  if (coverageProfiles.some((profile) => profile.name === "base"
    ? !validBasePackageEvidence(record, manifest.profiles.find((entry) => entry.name === "base"), candidate, environment)
    : !reusableProfile(record, profile, candidate, environment))) return null;
  const sourceRecord = manifest.merged.source_run_id
    ? referencedRun(record, manifest.merged.source_run_id, candidate, environment)
    : record;
  if (!sourceRecord) return null;
  try {
    const coverage = artifactFrom(sourceRecord, manifest.merged);
    return { ...record, coverage };
  } catch {
    return null;
  }
}

export function validAnalysisEvidence(record) {
  const analysis = record.manifest?.analysis;
  if (!analysis?.host || !analysis?.ce_task_id || !analysis?.report?.path || !analysis.report.sha256 || !Number.isSafeInteger(analysis.report.bytes)) return false;
  try {
    const path = safeRelative(record.runDir, analysis.report.path);
    if (statSync(path).size !== analysis.report.bytes || shaFile(path) !== analysis.report.sha256) return false;
    const report = validateReport(parseReport(path), analysis.host);
    return report.ce_task_id === analysis.ce_task_id && report.ce_task_url === analysis.ce_task_url && report.dashboard_url === analysis.dashboard_url;
  } catch {
    return false;
  }
}

function findCompleteCoverage(namespace, candidate, environment) {
  for (const record of manifestsIn(namespace)) {
    const complete = completeCoverage(record, candidate, environment);
    if (complete) return complete;
  }
  return null;
}

function resumableAnalysis(namespace, candidate, environment) {
  for (const record of manifestsIn(namespace)) {
    const { manifest } = record;
    if (!sameCandidate(manifest.candidate, candidate) || manifest.fingerprints?.coverage_environment !== environment.sha256) continue;
    if (!["submitted", "ce_running"].includes(manifest.analysis?.state) || !manifest.analysis.ce_task_id) continue;
    if (completeCoverage(record, candidate, environment) && validAnalysisEvidence(record)) return record;
  }
  return null;
}

function lockPath(namespace) {
  return join(namespace, "lock.json");
}

export function acquireLock(namespace, runId) {
  mkdirSync(namespace, { recursive: true });
  const path = lockPath(namespace);
  const lock = { run_id: runId, pid: process.pid, host: hostname(), started_at_utc: new Date().toISOString() };
  try {
    writeFileSync(path, `${JSON.stringify(lock)}\n`, { encoding: "utf8", mode: 0o600, flag: "wx" });
  } catch (error) {
    if (error.code === "EEXIST") throw new RunnerError(`A Sonar runner already owns this worktree: ${path}`);
    throw error;
  }
  return () => rmSync(path, { force: true });
}

function newManifest(runId, mode, candidate, environment, options, runDir) {
  const started = new Date().toISOString();
  return {
    schema_version: schemaVersion,
    run_id: runId,
    mode,
    started_at_utc: started,
    candidate: {
      repository_id: candidate.repository_id,
      worktree_id: candidate.worktree_id,
      repository_path: candidate.repository_path,
      head: candidate.head,
      tree: candidate.tree,
      inputs_sha256: candidate.inputs_sha256,
      inventory: candidate.inventory,
    },
    fingerprints: {
      coverage_environment: environment.sha256,
      profile_set: sha256(canonicalJson(coverageProfiles.map(profileDescriptor))),
      analysis_inputs: sha256(canonicalJson({ candidate: candidate.inputs_sha256, sonar_project: shaFile(join(candidate.repository_path, "sonar-project.properties")) })),
    },
    budgets: {
      overall_seconds: options.overallTimeout,
      coverage_seconds: options.coverageTimeout,
      profile_seconds: options.profileTimeout,
      scanner_seconds: options.scannerTimeout,
      quality_gate_seconds: options.qualityGateTimeout,
      command_seconds: commandTimeoutSeconds,
      cleanup_reserve_seconds: cleanupReserveSeconds,
      started_at_utc: started,
    },
    paths: { manifest: join(runDir, "manifest.json") },
    selection: { base_only: options.baseOnly, selected_profiles: options.baseOnly ? ["base"] : coverageProfiles.map((profile) => profile.name), required_profiles: coverageProfiles.length },
    work: coverageWorkPlan([], 0),
    profiles: [],
    merged: { status: "pending" },
    analysis: { state: "not_submitted", project_key: projectKey, attempts: [] },
    publication: { requested: options.publishStatus, state: options.publishStatus ? "pending" : "not_requested" },
    resources: { children: [], cleanup: { state: "pending", errors: [] } },
    result: { coverage: "incomplete", technical_gate: "incomplete", effect: options.publishStatus ? "pending" : "not_requested", disposition: "incomplete" },
  };
}

function createCampaign(namespace, mode, candidate, environment, options) {
  const runId = randomUUID();
  const runDir = join(namespace, "runs", runId);
  const manifest = newManifest(runId, mode, candidate, environment, options, runDir);
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  atomicWrite(join(runDir, "events.ndjson"), "");
  writeJson(join(runDir, "manifest.json"), manifest);
  return { runId, runDir, manifest };
}

function saveCampaign(campaign) {
  writeJson(join(campaign.runDir, "manifest.json"), campaign.manifest);
}

function event(campaign, phase, kind, details = {}) {
  appendFileSync(join(campaign.runDir, "events.ndjson"), `${JSON.stringify({ at_utc: new Date().toISOString(), run_id: campaign.manifest.run_id, phase, kind, ...details })}\n`, { encoding: "utf8", mode: 0o600 });
}

export function coverageWorkPlan(units, dedicatedProfiles) {
  const required = { race: 0, coverage: 0, dedicated: dedicatedProfiles };
  for (const unit of units) required[unit.phase] += 1;
  const phase = (count) => ({ completed: 0, required: count, reused: 0 });
  return {
    overall: phase(required.race + required.coverage + required.dedicated),
    race: phase(required.race),
    coverage: phase(required.coverage),
    dedicated: phase(required.dedicated),
  };
}

export class Progress {
  constructor(campaign, deadline, now = () => Date.now()) {
    this.campaign = campaign;
    this.deadline = deadline;
    this.now = now;
    this.startedMs = deadline.started;
    this.phase = "preflight";
    this.phaseStarted = this.startedMs;
    this.active = new Map();
    this.work = coverageWorkPlan([], 0);
    this.currentWorkPhase = "race";
    this.lastProgressMs = this.now();
    this.lastProgress = new Date(this.lastProgressMs).toISOString();
    this.lastProgressAction = null;
    this.lastProgressPackage = null;
    this.lastProgressTest = null;
    this.lastOutputMs = null;
    this.lastOutput = null;
    this.timer = null;
  }

  workDetails() {
    const phase = this.work[this.currentWorkPhase] || this.work.overall;
    return {
      work_phase: this.currentWorkPhase,
      phase_completed: phase.completed,
      phase_required: phase.required,
      phase_reused: phase.reused,
      overall_completed: this.work.overall.completed,
      overall_required: this.work.overall.required,
      overall_reused: this.work.overall.reused,
    };
  }

  syncWork() {
    this.campaign.manifest.work = structuredClone(this.work);
    saveCampaign(this.campaign);
  }

  configureWork(work) {
    this.work = structuredClone(work);
    this.currentWorkPhase = this.work.race.required ? "race" : this.work.coverage.required ? "coverage" : "dedicated";
    this.syncWork();
    event(this.campaign, "coverage", "work_planned", this.workDetails());
  }

  complete(workPhase, { reused = false } = {}) {
    const phase = this.work[workPhase];
    if (!phase) throw new RunnerError(`Unknown work phase: ${workPhase}`);
    if (phase.completed >= phase.required || this.work.overall.completed >= this.work.overall.required) {
      throw new RunnerError(`Work progress exceeds declared denominator for ${workPhase}`);
    }
    phase.completed += 1;
    this.work.overall.completed += 1;
    if (reused) {
      phase.reused += 1;
      this.work.overall.reused += 1;
    }
    this.currentWorkPhase = workPhase;
    this.syncWork();
    event(this.campaign, this.phase, "work_completed", { ...this.workDetails(), reused });
  }

  meaningful(phase, details = {}) {
    const now = this.now();
    if (phase !== this.phase) this.phaseStarted = now;
    this.phase = phase;
    this.lastProgressMs = now;
    this.lastProgress = new Date(now).toISOString();
    event(this.campaign, phase, "progress", { ...details, ...this.workDetails(), observed_at_utc: this.lastProgress, elapsed_ms: now - this.startedMs, phase_elapsed_ms: now - this.phaseStarted, last_progress_utc: this.lastProgress });
  }

  activate(profile, deadlineAt = null, workPhase = "dedicated") {
    const now = this.now();
    this.currentWorkPhase = workPhase;
    this.active.set(profile, { started_ms: now, deadline_at_ms: deadlineAt, work_phase: workPhase, current_test: null, current_package: null, last_progress_ms: now, last_progress_utc: new Date(now).toISOString(), last_progress_action: "profile_started", last_progress_package: null, last_progress_test: null });
  }

  output() {
    this.lastOutputMs = this.now();
    this.lastOutput = new Date(this.lastOutputMs).toISOString();
  }

  location(profile, packageName, testName) {
    const active = this.active.get(profile);
    if (!active) return;
    if (packageName) {
      active.current_package = packageName;
      active.current_test = testName || null;
    } else if (testName) {
      active.current_test = testName;
    }
  }

  semantic(profile, transition) {
    const active = this.active.get(profile);
    if (!active) return;
    const now = this.now();
    this.currentWorkPhase = active.work_phase;
    this.location(profile, transition.package, transition.test);
    active.last_progress_ms = now;
    active.last_progress_utc = new Date(now).toISOString();
    active.last_progress_action = transition.action;
    active.last_progress_package = transition.package;
    active.last_progress_test = transition.test;
    this.lastProgressMs = now;
    this.lastProgress = active.last_progress_utc;
    this.lastProgressAction = transition.action;
    this.lastProgressPackage = transition.package;
    this.lastProgressTest = transition.test;
    event(this.campaign, this.phase, "progress", { profile, observation: "go-lifecycle", action: transition.action, package: transition.package, test: transition.test, ...this.workDetails(), observed_at_utc: this.lastProgress, elapsed_ms: now - this.startedMs, phase_elapsed_ms: now - this.phaseStarted, last_progress_utc: this.lastProgress });
  }

  deactivate(profile) {
    this.active.delete(profile);
  }

  heartbeat() {
    const now = this.now();
    const age = now - this.lastProgressMs;
    const activeProfiles = [...this.active.entries()].map(([name, value]) => ({
      profile: name,
      work_phase: value.work_phase,
      elapsed_ms: now - value.started_ms,
      deadline_remaining_ms: value.deadline_at_ms === null ? this.deadline.remaining(this.phase) : Math.max(0, value.deadline_at_ms - now),
      current_test: value.current_test,
      current_package: value.current_package,
      last_progress_action: value.last_progress_action,
      last_progress_package: value.last_progress_package,
      last_progress_test: value.last_progress_test,
      last_progress_utc: value.last_progress_utc,
      last_progress_age_ms: now - value.last_progress_ms,
      semantic_idle: now - value.last_progress_ms >= 120000,
    }));
    const semanticIdle = age >= 120000;
    const kind = semanticIdle ? "stalled" : "heartbeat";
    event(this.campaign, this.phase, kind, {
      head: this.campaign.manifest.candidate.head,
      active_profiles: activeProfiles,
      ...this.workDetails(),
      elapsed_ms: now - this.startedMs,
      phase_elapsed_ms: now - this.phaseStarted,
      deadline_remaining_ms: this.deadline.remaining(this.phase),
      last_progress_utc: this.lastProgress,
      last_progress_age_ms: age,
      last_progress_action: this.lastProgressAction,
      last_progress_package: this.lastProgressPackage,
      last_progress_test: this.lastProgressTest,
      last_output_utc: this.lastOutput,
      last_output_age_ms: this.lastOutputMs === null ? null : now - this.lastOutputMs,
      ce_status: this.campaign.manifest.analysis?.last_ce_status || null,
      semantic_idle: semanticIdle,
      stall_reason: semanticIdle ? "no_semantic_transition_observed" : null,
    });
    const activeSummary = activeProfiles.map((profile) => `${profile.profile}:${profile.last_progress_action || "none"}:${profile.last_progress_age_ms}ms`).join(",");
    const work = this.workDetails();
    console.log(`sonar run ${this.campaign.manifest.run_id}: ${this.phase} ${work.work_phase} ${work.phase_completed}/${work.phase_required}; overall ${work.overall_completed}/${work.overall_required}; semantic_idle=${semanticIdle}; active=${activeSummary || "none"}; diagnostics ${this.campaign.runDir}`);
    return { kind, active_profiles: activeProfiles, semantic_idle: semanticIdle, ...work };
  }

  start() {
    this.timer = setInterval(() => this.heartbeat(), 10000);
    this.timer.unref?.();
  }

  stop() {
    clearInterval(this.timer);
  }
}

export class Deadline {
  constructor(options) {
    this.started = Date.now();
    this.overall = this.started + options.overallTimeout * 1000;
    this.limits = { coverage: options.coverageTimeout * 1000, scanner: options.scannerTimeout * 1000, quality: options.qualityGateTimeout * 1000 };
    this.profile = options.profileTimeout * 1000;
    this.phaseDeadlines = { coverage: null, scanner: null, quality: null };
  }

  phaseFor(phase) {
    if (phase === "scanner") return "scanner";
    if (["ce", "quality", "quality-gate"].includes(phase)) return "quality";
    return "coverage";
  }

  begin(phase) {
    const resolved = this.phaseFor(phase);
    if (!this.phaseDeadlines[resolved]) this.phaseDeadlines[resolved] = Date.now() + this.limits[resolved];
    return resolved;
  }

  remaining(phase) {
    const resolved = this.phaseFor(phase);
    const phaseDeadline = this.phaseDeadlines[resolved] || (Date.now() + this.limits[resolved]);
    return Math.max(0, Math.min(this.overall - Date.now() - cleanupReserveSeconds * 1000, phaseDeadline - Date.now()));
  }

  timeoutFor(phase, ownMs) {
    const resolved = this.begin(phase);
    const timeout = Math.min(ownMs, this.phaseDeadlines[resolved] - Date.now(), this.overall - Date.now() - cleanupReserveSeconds * 1000);
    if (timeout <= 0) throw new BudgetError(`${resolved} budget exhausted`);
    return timeout;
  }

  commandTimeout(ownMs) {
    const timeout = Math.min(ownMs, this.overall - Date.now() - cleanupReserveSeconds * 1000);
    if (timeout <= 0) throw new BudgetError("Overall runner budget exhausted; cleanup reserve retained");
    return timeout;
  }
}

async function wait(milliseconds, signal) {
  if (signal?.aborted) throw signal.reason;
  return new Promise((resolveWait, rejectWait) => {
    const timeout = setTimeout(resolveWait, milliseconds);
    const abort = () => {
      clearTimeout(timeout);
      rejectWait(signal.reason);
    };
    signal?.addEventListener("abort", abort, { once: true });
  });
}

async function waitForPostgres(dockerCommand, containerId, cwd, execution, deadline) {
  const until = Date.now() + dockerReadyTimeoutSeconds * 1000;
  while (Date.now() < until) {
    try {
      await capture(dockerCommand, ["exec", containerId, "pg_isready", "--username", databaseUser, "--dbname", "postgres"], cwd, {
        ...execution,
        timeoutMs: Math.min(5000, deadline.timeoutFor("coverage", 5000)),
      });
      return;
    } catch (error) {
      if (error instanceof SignalError || error instanceof BudgetError) throw error;
      await wait(1000, execution.signal);
    }
  }
  throw new BudgetError("Timed out waiting for the runner-owned PostgreSQL container");
}

async function startPostgres(dockerCommand, cwd, imageId, campaign, execution, deadline) {
  const name = `engram-sonarqube-${campaign.manifest.run_id.replaceAll("-", "").slice(0, 20)}`;
  let containerId = null;
  const remove = async () => {
    const cleanup = cleanupExecution(execution);
    const inspected = await capture(dockerCommand, ["inspect", "--format", "{{.Id}} {{index .Config.Labels \"engram.sonar.run\"}}", containerId], cwd, { ...cleanup, timeoutMs: commandTimeoutSeconds * 1000 });
    if (!inspected.startsWith(containerId) || !inspected.endsWith(campaign.manifest.run_id)) throw new RunnerError("Refusing to remove a PostgreSQL container without the current runner ownership label");
    await capture(dockerCommand, ["rm", "--force", "--volumes", containerId], cwd, { ...cleanup, timeoutMs: commandTimeoutSeconds * 1000 });
    campaign.manifest.resources.container.state = "removed";
    saveCampaign(campaign);
  };
  try {
    const output = await capture(dockerCommand, [
      "run", "--detach", "--name", name, "--publish", "127.0.0.1::5432",
      "--label", "engram.sonar.owner=run-sonarqube", "--label", `engram.sonar.run=${campaign.manifest.run_id}`,
      "--env", `POSTGRES_USER=${databaseUser}`, "--env", `POSTGRES_PASSWORD=${databasePassword}`, "--env", "POSTGRES_DB=postgres", imageId,
    ], cwd, { ...execution, timeoutMs: deadline.timeoutFor("coverage", commandTimeoutSeconds * 1000), label: "PostgreSQL startup" });
    containerId = output.trim();
    if (!/^[0-9a-f]{12,64}$/i.test(containerId)) throw new RunnerError("Docker did not return a container ID for the runner-owned PostgreSQL container");
    campaign.manifest.resources.container = { id: containerId, name, image_id: imageId, owner_label: campaign.manifest.run_id, state: "running" };
    saveCampaign(campaign);
    await waitForPostgres(dockerCommand, containerId, cwd, execution, deadline);
    const port = parseDockerPort(await capture(dockerCommand, ["port", containerId, "5432/tcp"], cwd, { ...execution, timeoutMs: deadline.timeoutFor("coverage", commandTimeoutSeconds * 1000) }));
    let serial = 0;
    let provisioning = Promise.resolve();
    return {
      async createDatabase(prefix, profileDeadline) {
        serial += 1;
        const database = `${prefix}_${campaign.manifest.run_id.replaceAll("-", "").slice(0, 16)}_${serial}`;
        if (!/^[a-z][a-z0-9_]{0,62}$/.test(database)) throw new RunnerError(`Invalid runner database name: ${database}`);
        const current = provisioning.then(async () => {
          const timeoutMs = profileTimeout(deadline, profileDeadline, commandTimeoutSeconds * 1000);
          await capture(dockerCommand, ["exec", containerId, "createdb", "--username", databaseUser, database], cwd, { ...execution, timeoutMs });
          await capture(dockerCommand, ["exec", containerId, "psql", "--username", databaseUser, "--dbname", database, "--command", "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;"], cwd, { ...execution, timeoutMs: profileTimeout(deadline, profileDeadline, commandTimeoutSeconds * 1000) });
        });
        provisioning = current.catch(() => { });
        await current;
        return `postgres://${databaseUser}:${databasePassword}@127.0.0.1:${port}/${database}?sslmode=disable`;
      },
      stop: remove,
    };
  } catch (error) {
    if (containerId && campaign.manifest.resources.container?.state === "running") {
      try { await remove(); } catch (cleanupError) {
        campaign.manifest.resources.cleanup.errors.push(cleanupError.message);
        saveCampaign(campaign);
      }
    }
    throw error;
  }
}
function profileEnvironment(baseEnvironment, profile, databaseDSN) {
  if (!databaseDSN) return baseEnvironment;
  const environment = { ...baseEnvironment };
  if (profile.databaseEnvironment === installedUCIDatabaseEnv) environment[installedUCIDatabaseEnv] = databaseDSN;
  else if (profile.databaseEnvironment === hapFixtureDatabaseEnv) environment[hapFixtureDatabaseEnv] = databaseDSN;
  else environment.DATABASE_DSN = databaseDSN;
  return environment;
}

function testIdentity(packageName, test) {
  return `${packageName}/${test}`;
}

function parseTestList(output, profile) {
  const run = profile.run ? new RegExp(profile.run) : null;
  const skip = profile.skip ? new RegExp(profile.skip) : null;
  const tests = [];
  let pending = [];
  for (const raw of output.split(/\r?\n/)) {
    const line = raw.trim();
    if (/^Test/.test(line)) {
      pending.push(line);
      continue;
    }
    const packageMatch = /^ok\s+(\S+)/.exec(line);
    if (!packageMatch) continue;
    for (const test of pending) {
      if ((!run || run.test(test)) && (!skip || !skip.test(test))) tests.push({ package: packageMatch[1], test });
    }
    pending = [];
  }
  return tests.sort((left, right) => testIdentity(left.package, left.test).localeCompare(testIdentity(right.package, right.test)));
}

export function profileTimeout(deadline, profileDeadline, ownMs) {
  const timeout = Math.min(deadline.timeoutFor("profile", ownMs), profileDeadline - Date.now());
  if (timeout <= 0) throw new BudgetError("profile budget exhausted");
  return timeout;
}

export function classifyProfileFailure(error, execution) {
  if (error instanceof BudgetError) return { status: "timed_out", reason: "profile_budget_exhausted" };
  if (execution.signal?.aborted) return { status: "cancelled", reason: "runner_cancelled" };
  return { status: "failed", reason: "command_failed" };
}

async function expectedTests(goCommand, profile, cwd, environment, execution, deadline, profileDeadline) {
  const result = await runProcess(goCommand, testInventoryArguments(profile), {
    ...execution,
    cwd,
    env: environment,
    timeoutMs: profileTimeout(deadline, profileDeadline, commandTimeoutSeconds * 1000),
    label: `${profile.name} test inventory`,
  });
  const tests = parseTestList(result.stdout, profile);
  if (!tests.length) throw new RunnerError(`${profile.name} selected no tests`);
  return tests;
}

function goLifecycleTransition(state, eventValue) {
  const action = eventValue.Action;
  const packageName = eventValue.Package || null;
  const test = eventValue.Test || null;
  const terminal = ["pass", "fail", "skip"].includes(action);
  const meaningful = (action === "start" && packageName && !test) ||
    (action === "run" && packageName && test) ||
    (["pause", "cont"].includes(action) && packageName && test) ||
    (terminal && packageName);
  if (!meaningful) return null;
  state.lifecycle ||= new Set();
  const key = `${action}\u0000${packageName}\u0000${test || ""}`;
  if (state.lifecycle.has(key)) return null;
  state.lifecycle.add(key);
  return { action, package: packageName, test };
}

export function consumeGoEvents(chunk, state, write, observe = () => { }) {
  state.buffer += chunk;
  const lines = state.buffer.split(/\r?\n/);
  state.buffer = lines.pop();
  for (const line of lines) {
    if (!line.trim()) continue;
    write(`${line}\n`);
    let eventValue;
    try { eventValue = JSON.parse(line); } catch { continue; }
    if (eventValue.Package) {
      state.currentPackage = eventValue.Package;
      state.currentTest = eventValue.Test || null;
    } else if (eventValue.Test) {
      state.currentTest = eventValue.Test;
    }
    if (eventValue.Test) {
      const key = testIdentity(eventValue.Package || "", eventValue.Test);
      if (eventValue.Action === "output") state.skipReasons.set(key, `${state.skipReasons.get(key) || ""}${eventValue.Output || ""}`);
      if (eventValue.Action === "pass") state.passedTests.add(key);
      if (eventValue.Action === "skip") state.skippedTests.set(key, { package: eventValue.Package || "", test: eventValue.Test, reason: state.skipReasons.get(key)?.trim() || "" });
      if (eventValue.Action === "fail") state.failedTests.add(key);
      if (eventValue.Action === "run") state.startedTests.add(key);
    }
    if (eventValue.Package && !eventValue.Test && eventValue.Action === "pass") state.passedPackages.add(eventValue.Package);
    if (eventValue.Package && !eventValue.Test && eventValue.Action === "fail") state.failedPackages.add(eventValue.Package);
    observe(eventValue, goLifecycleTransition(state, eventValue));
  }
}

export function finishGoEvents(state, write, observe = () => { }) {
  const buffered = state.buffer;
  state.buffer = "";
  if (buffered.trim()) consumeGoEvents(`${buffered}\n`, state, write, observe);
}

export function summarizeGoEvents(events) {
  const state = { buffer: "", currentTest: null, currentPackage: null, passedTests: new Set(), skippedTests: new Map(), skipReasons: new Map(), failedTests: new Set(), startedTests: new Set(), passedPackages: new Set(), failedPackages: new Set() };
  consumeGoEvents(events, state, () => { });
  finishGoEvents(state, () => { });
  return { passed_tests: [...state.passedTests], passed_packages: [...state.passedPackages], failed_tests: [...state.failedTests], skipped_tests: [...state.skippedTests.keys()] };
}

function skipObligation(profile, skipped) {
  if (profile.name !== "base" && !profile.baseUnit) return null;
  const rootTest = skipped.test.split("/")[0];
  const owner = coverageProfiles.find((candidate) => {
    if (!candidate.databasePrefix || candidate.name === profile.name) return false;
    if (!skipped.package.endsWith(`/${candidate.target.slice(2)}`)) return false;
    if (candidate.run && !new RegExp(candidate.run).test(rootTest)) return false;
    return !candidate.skip || !new RegExp(candidate.skip).test(rootTest);
  });
  return owner ? { test: skipped.test, package: skipped.package, required_profile: owner.name } : null;
}

function functionBody(source, name) {
  const match = new RegExp(`func\\s+${name}\\s*\\([^)]*\\)[^{]*\\{`).exec(source);
  if (!match) return null;
  let depth = 1;
  for (let index = match.index + match[0].length; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    if (source[index] === "}") depth -= 1;
    if (depth === 0) return source.slice(match.index, index + 1);
  }
  return null;
}

function skipfPattern(template) {
  const placeholders = /%(?:[-+ #0]*\d*(?:\.\d+)?[vTtbcdoOqxXUeEfFgGsp])/g;
  const pieces = template.split(placeholders);
  return new RegExp(pieces.map((piece) => piece.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*?"));
}

function reachableSkipMatchers(source, test) {
  const pending = [test];
  const visited = new Set();
  const matchers = [];
  while (pending.length) {
    const name = pending.pop();
    if (visited.has(name)) continue;
    visited.add(name);
    const body = functionBody(source, name);
    if (!body) continue;
    for (const match of body.matchAll(/\b[A-Za-z_]\w*\.Skip\("([^"]+)"/g)) {
      matchers.push({ source_reason: match[1], matches: (reason) => reason.includes(match[1]) });
    }
    for (const match of body.matchAll(/\b[A-Za-z_]\w*\.Skipf\("([^"]+)"/g)) {
      const pattern = skipfPattern(match[1]);
      matchers.push({ source_reason: match[1], matches: (reason) => pattern.test(reason) });
    }
    for (const match of body.matchAll(/\b([A-Za-z_]\w*)\s*\(/g)) {
      if (!visited.has(match[1]) && functionBody(source, match[1])) pending.push(match[1]);
    }
  }
  return matchers;
}

function conditionalSkipObligation(profile, skipped, candidate) {
  if (profile.name !== "base" && !profile.baseUnit) return null;
  const packageSuffix = skipped.package.replace(/^.*?(?=\/internal\/|\/cmd\/|\/pkg\/)/, "");
  const sources = [];
  for (const inventory of candidate.inventory || []) {
    const sourceDirectory = dirname(inventory.path).replaceAll("\\", "/");
    if (inventory.type !== "file" || !inventory.path.endsWith("_test.go")) continue;
    if (profile.unitDirectory ? sourceDirectory !== profile.unitDirectory : !packageSuffix.endsWith(`/${sourceDirectory}`)) continue;
    const sourcePath = join(candidate.repository_path, inventory.path);
    if (!existsSync(sourcePath) || shaFile(sourcePath) !== inventory.sha256) continue;
    sources.push({ inventory, source: readFileSync(sourcePath, "utf8") });
  }
  const rootTest = skipped.test.split("/")[0];
  const root = sources.find((item) => functionBody(item.source, rootTest));
  if (!root) return null;
  const matcher = reachableSkipMatchers(sources.map((item) => item.source).join("\n"), rootTest).find((item) => item.matches(skipped.reason));
  if (!matcher) return null;
  return { kind: "conditional", package: skipped.package, test: skipped.test, source: root.inventory.path, source_sha256: root.inventory.sha256, source_reason: matcher.source_reason, reason: skipped.reason };
}

function skipAdmission(profile, skipped, candidate) {
  return skipObligation(profile, skipped) || conditionalSkipObligation(profile, skipped, candidate);
}

export async function runCoverageProfile(goCommand, profile, campaign, entry, cwd, baseEnvironment, postgres, execution, deadline, progress, secrets, profileDeadline, candidate, runtime = {}) {
  const started = Date.now();
  const deadlineAt = profileDeadline || started + deadline.profile;
  const attemptDirectory = join(campaign.runDir, "profiles", profile.name, `attempt-${entry.attempt}`);
  mkdirSync(attemptDirectory, { recursive: true, mode: 0o700 });
  const coveragePath = join(attemptDirectory, "coverage.out");
  const eventPath = join(attemptDirectory, "test-events.ndjson");
  const stderrPath = join(attemptDirectory, "stderr.log");
  const state = { buffer: "", currentTest: null, currentPackage: null, passedTests: new Set(), skippedTests: new Map(), skipReasons: new Map(), failedTests: new Set(), startedTests: new Set(), passedPackages: new Set(), failedPackages: new Set(), lifecycle: new Set() };
  const coveragePhase = profile.unitPhase !== "race";
  const args = [...profileDescriptor(profile).effective_argv, ...(coveragePhase ? [`-coverprofile=${relative(cwd, coveragePath)}`] : [])];
  const observeGoEvent = (eventValue, transition) => {
    progress.location(profile.name, eventValue.Package || null, eventValue.Test || null);
    if (transition) progress.semantic(profile.name, transition);
  };
  let testWriter = null;
  let stderrWriter = null;
  entry.started_at_utc = new Date().toISOString();
  entry.status = "running";
  entry.inventory = { state: "running" };
  entry.effective_argv = args.filter((argument) => !argument.includes(databasePassword));
  saveCampaign(campaign);
  progress.activate(profile.name, deadlineAt, profile.workPhase || "dedicated");
  progress.meaningful("coverage", { profile: profile.name, state: "inventory_started" });
  try {
    const databaseDSN = profile.databasePrefix ? await postgres.createDatabase(profile.databasePrefix, deadlineAt) : null;
    const environment = profileEnvironment(baseEnvironment, profile, databaseDSN);
    const tests = await (runtime.expectedTests || expectedTests)(goCommand, profile, cwd, environment, execution, deadline, deadlineAt);
    entry.expected_test_inventory_sha256 = sha256(canonicalJson(tests));
    entry.expected_tests = tests;
    entry.inventory = { state: "passed", expected_test_inventory_sha256: entry.expected_test_inventory_sha256, test_count: tests.length };
    saveCampaign(campaign);
    testWriter = createLogWriter(eventPath, secrets);
    stderrWriter = createLogWriter(stderrPath, secrets);
    await (runtime.runProcess || runProcess)(goCommand, args, {
      ...execution,
      cwd,
      env: environment,
      timeoutMs: profileTimeout(deadline, deadlineAt, deadline.profile),
      label: `${profile.name} coverage`,
      onStdout: (chunk) => {
        consumeGoEvents(chunk, state, (line) => testWriter.write(line), observeGoEvent);
        progress.output();
      },
      onStderr: (chunk) => {
        stderrWriter.write(chunk);
        progress.output();
      },
    });
    finishGoEvents(state, (line) => testWriter.write(line), observeGoEvent);
    testWriter.finish();
    stderrWriter.finish();
    entry.test_events = { path: relative(campaign.runDir, eventPath), sha256: shaFile(eventPath), bytes: statSync(eventPath).size };
    entry.stderr = { path: relative(campaign.runDir, stderrPath), sha256: shaFile(stderrPath), bytes: statSync(stderrPath).size };
    entry.package_counts = { passed: state.passedPackages.size, failed: state.failedPackages.size, tests_passed: state.passedTests.size, tests_skipped: state.skippedTests.size };
    entry.unexpected_skip_count = 0;
    const coverage = coveragePhase ? validateCoverage(coveragePath, null, { allowZeroCoverableStatements: true }) : null;
    const evidence = evaluateTestEvidence({ runDir: campaign.runDir, manifest: campaign.manifest }, entry, profile, candidate, baseEnvironment);
    if (evidence.execution !== "passed" || evidence.local_artifact !== "passed") throw new RunnerError(`${profile.name} ${evidence.reason}`);
    if (coverage?.coverage_classification === "zero_coverable_statements" && !zeroCoverableEvidence(evidence, entry)) throw new RunnerError(`${profile.name} did not prove zero coverable statements`);
    if (coverage) entry.coverage = { path: relative(campaign.runDir, coveragePath), ...coverage };
    else entry.coverage = { status: "not_applicable" };
    entry.allowed_skip_obligations = evidence.allowed_skip_obligations;
    entry.status = "passed";
    entry.completed_at_utc = new Date().toISOString();
    entry.elapsed_ms = Date.now() - started;
    progress.complete(profile.workPhase || "dedicated");
    progress.meaningful("coverage", { profile: profile.name, state: "passed", elapsed_ms: entry.elapsed_ms, campaign_obligations: evidence.campaign_obligations });
  } catch (error) {
    if (testWriter) finishGoEvents(state, (line) => testWriter.write(line), observeGoEvent);
    testWriter?.finish();
    stderrWriter?.finish();
    const failure = classifyProfileFailure(error, execution);
    entry.status = failure.status;
    entry.failure_reason = failure.reason;
    entry.inventory = { state: "failed", failure_reason: failure.reason };
    entry.completed_at_utc = new Date().toISOString();
    entry.elapsed_ms = Date.now() - started;
    entry.failure = redacted(error.message, secrets);
    entry.package_counts = { passed: state.passedPackages.size, failed: state.failedPackages.size, tests_passed: state.passedTests.size, tests_skipped: state.skippedTests.size };
    entry.unexpected_skip_count = [...state.skippedTests.values()].filter((skipped) => !skipObligation(profile, skipped)).length;
    if (existsSync(coveragePath)) entry.coverage = { path: relative(campaign.runDir, coveragePath), sha256: shaFile(coveragePath), bytes: statSync(coveragePath).size };
    if (existsSync(eventPath)) entry.test_events = { path: relative(campaign.runDir, eventPath), sha256: shaFile(eventPath), bytes: statSync(eventPath).size };
    if (existsSync(stderrPath)) entry.stderr = { path: relative(campaign.runDir, stderrPath), sha256: shaFile(stderrPath), bytes: statSync(stderrPath).size };
    throw error;
  } finally {
    progress.deactivate(profile.name);
    saveCampaign(campaign);
  }
}
export async function scheduleProfiles(profiles, jobs, runProfile) {
  if (![1, 2].includes(jobs)) throw new RunnerError("jobs must be 1 or 2");
  const outcomes = new Map();
  for (const profile of profiles) {
    try { outcomes.set(profile.name, await runProfile(profile)); }
    catch (error) { outcomes.set(profile.name, { status: "failed", error }); return outcomes; }
  }
  return outcomes;
}
export async function schedulePackageUnits(units, jobs, runUnit) {
  if (![1, 2].includes(jobs)) throw new RunnerError("jobs must be 1 or 2");
  const outcomes = new Map();
  const run = async (unit) => {
    try { outcomes.set(unit.id, await runUnit(unit)); }
    catch (error) { outcomes.set(unit.id, { status: "failed", error }); }
  };
  for (const phase of ["race", "coverage"]) {
    const phaseUnits = units.filter((unit) => unit.phase === phase);
    for (const unit of phaseUnits.filter((unit) => unit.classification === "serial")) await run(unit);
    const ordinary = phaseUnits.filter((unit) => unit.classification === "ordinary");
    let next = 0;
    await Promise.all(Array.from({ length: Math.min(jobs, ordinary.length) }, async () => {
      while (next < ordinary.length) {
        const unit = ordinary[next];
        next += 1;
        await run(unit);
      }
    }));
    if (phase === "race" && [...outcomes.values()].some((outcome) => outcome.status === "failed")) return outcomes;
  }
  return outcomes;
}

export function terminalPackageSummary(entries) {
  const summary = { total: entries.length, race: 0, coverage: 0, passed: 0, failed: 0, timed_out: 0, cancelled: 0, pending: 0 };
  for (const entry of entries) {
    if (entry.unit?.phase === "race") summary.race += 1;
    if (entry.unit?.phase === "coverage") summary.coverage += 1;
    if (Object.hasOwn(summary, entry.status)) summary[entry.status] += 1;
    else summary.pending += 1;
  }
  return summary;
}

export function assertUnitDedicatedOwnership(units, record, candidate, environment) {
  for (const unit of units) {
    if (unit.status !== "passed") throw new RunnerError(`${unit.id} did not retain a passed package unit`);
    const evidence = evaluateTestEvidence(record, unit, packageUnitProfile(unit.unit), candidate, environment);
    if (evidence.execution !== "passed" || evidence.local_artifact !== "passed") throw new RunnerError(`${unit.id} did not retain admissible test evidence`);
    if (evidence.campaign_obligations !== "passed") {
      const obligation = evidence.allowed_skip_obligations.find((item) => item.kind !== "conditional");
      throw new RunnerError(`${unit.id} skipped ${obligation.package}/${obligation.test} without a revalidated passed dedicated ${obligation.required_profile} profile`);
    }
  }
}

function packageUnitProfile(unit) {
  return {
    name: unit.id,
    target: unit.importPath,
    race: unit.phase === "race",
    coverpkg: unit.phase === "coverage" ? unit.coverpkg.join(",") : null,
    packageConcurrency: 1,
    resourceGroup: unit.classification === "serial" ? "exclusive" : "ordinary-package",
    baseUnit: true,
    unitPhase: unit.phase,
    workPhase: unit.phase,
    unitDirectory: unit.directory,
  };
}

function fingerprintPackageUnit(unit, candidate, environment) {
  return sha256(canonicalJson({
    unit,
    candidate_inputs_sha256: candidate.inputs_sha256,
    environment_sha256: environment.sha256,
    runner_sha256: shaFile(scriptPath),
  }));
}

function retainedUnitEvidence(record, entry, unit, candidate, environment) {
  const visited = new Set();
  let sourceRecord = record;
  let sourceEntry = entry;
  while (sourceEntry?.source_run_id) {
    if (visited.has(sourceEntry.source_run_id)) return null;
    visited.add(sourceEntry.source_run_id);
    sourceRecord = referencedRun(sourceRecord, sourceEntry.source_run_id, candidate, environment);
    sourceEntry = sourceRecord?.manifest?.profiles?.find((profile) => profile.name === "base")?.units?.find((item) => item.id === unit.id);
    if (!sourceRecord || !sourceEntry) return null;
  }
  return sourceRecord && sourceEntry ? { record: sourceRecord, entry: sourceEntry } : null;
}

function validPackageUnitEvidence(record, entry, unit, candidate, environment) {
  if (!entry || entry.status !== "passed" || entry.fingerprint !== fingerprintPackageUnit(unit, candidate, environment)) return false;
  const source = retainedUnitEvidence(record, entry, unit, candidate, environment);
  if (!source || source.entry.status !== "passed" || source.entry.fingerprint !== fingerprintPackageUnit(unit, candidate, environment)) return false;
  const evidence = evaluateTestEvidence(source.record, source.entry, packageUnitProfile(unit), candidate, environment);
  if (evidence.execution !== "passed" || evidence.local_artifact !== "passed" || evidence.campaign_obligations !== "passed") return false;
  try {
    if (unit.phase === "coverage") artifactFrom(source.record, source.entry.coverage, { allowZeroCoverableStatements: zeroCoverableEvidence(evidence, source.entry) });
    return unit.phase !== "coverage" || source.entry.coverage?.status !== "not_applicable";
  } catch {
    return false;
  }
}

function validBasePackageEvidence(record, entry, candidate, environment) {
  return entry?.status === "passed" && Array.isArray(entry.units) && entry.units.length > 0 && entry.units.every((unitEntry) => validPackageUnitEvidence(record, unitEntry, unitEntry.unit, candidate, environment));
}

function findReusablePackageUnits(namespace, units, candidate, environment) {
  const found = new Map();
  for (const record of manifestsIn(namespace)) {
    const base = record.manifest.profiles?.find((entry) => entry.name === "base");
    for (const unit of units) {
      if (found.has(unit.id)) continue;
      const entry = base?.units?.find((item) => item.id === unit.id);
      if (validPackageUnitEvidence(record, entry, unit, candidate, environment)) found.set(unit.id, { entry, sourceRun: record.manifest.run_id });
    }
  }
  return found;
}

function packageUnitCoveragePath(campaign, entry) {
  if (!entry.source_run_id) return safeRelative(campaign.runDir, entry.coverage.path);
  const source = manifestsIn(campaign.namespace).find((record) => record.manifest.run_id === entry.source_run_id);
  return artifactFrom(source, entry.coverage, { allowZeroCoverableStatements: entry.coverage?.coverage_classification === "zero_coverable_statements" }).path;
}

async function runBasePackageUnits(baseEntry, campaign, candidate, environment, options, execution, deadline, progress, goCommand, runtime) {
  const profileDeadline = Date.now() + deadline.profile;
  baseEntry.status = "running";
  baseEntry.started_at_utc = new Date().toISOString();
  baseEntry.units = [];
  saveCampaign(campaign);
  let units;
  try {
    const discoveredUnits = runtime.packageUnits ?? await discoverPackageUnits(goCommand, candidate.repository_path, execution, deadline, profileDeadline, options.baseOnly);
    units = options.baseOnly ? discoveredUnits.filter((unit) => unit.core) : discoveredUnits;
    if (!units.length) throw new RunnerError("base selected no first-party packages with tests");
    baseEntry.unit_plan = { selected_units: units.length, race_units: units.filter((unit) => unit.phase === "race").length, coverage_units: units.filter((unit) => unit.phase === "coverage").length, core_only: options.baseOnly };
    const dedicatedEntries = options.baseOnly ? [] : campaign.manifest.profiles.filter((entry) => entry.name !== "base");
    progress.configureWork(coverageWorkPlan(units, dedicatedEntries.length));
    for (const entry of dedicatedEntries.filter((entry) => entry.source_run_id)) progress.complete("dedicated", { reused: true });
    const reusable = options.fresh ? new Map() : findReusablePackageUnits(campaign.namespace, units, candidate, environment);
    for (const unit of units) {
      const profile = packageUnitProfile(unit);
      const cached = reusable.get(unit.id);
      const entry = { id: unit.id, unit, name: unit.id, descriptor: profileDescriptor(profile), fingerprint: fingerprintPackageUnit(unit, candidate, environment), status: "pending", attempt: 1, classification: unit.classification, invalidation_reasons: cached ? [] : [options.fresh ? "fresh-requested" : "no-exact-valid-attempt"] };
      if (cached) {
        Object.assign(entry, cached.entry, { source_run_id: cached.sourceRun, source_artifact: cached.entry.coverage?.status === "not_applicable" ? null : { path: cached.entry.coverage.path, sha256: cached.entry.coverage.sha256, bytes: cached.entry.coverage.bytes, coverage_classification: cached.entry.coverage.coverage_classification } });
        progress.complete(unit.phase, { reused: true });
        event(campaign, "coverage", "unit_reused", { unit: unit.id, package: unit.importPath, source_run_id: cached.sourceRun });
      }
      baseEntry.units.push(entry);
    }
    saveCampaign(campaign);
    const entries = new Map(baseEntry.units.map((entry) => [entry.id, entry]));
    const outcomes = await schedulePackageUnits(units.filter((unit) => !reusable.has(unit.id)), options.jobs, async (unit) => {
      const profile = packageUnitProfile(unit);
      await runCoverageProfile(goCommand, profile, campaign, entries.get(unit.id), candidate.repository_path, environment.testEnvironment, null, execution, deadline, progress, [process.env.SONAR_TOKEN, process.env.DATABASE_DSN, databasePassword], Date.now() + deadline.profile, candidate, runtime.unitRuntime);
      return { status: "passed" };
    });
    baseEntry.unit_summary = terminalPackageSummary(baseEntry.units);
    const failed = [...outcomes.values()].find((outcome) => outcome.status === "failed");
    if (failed) throw failed.error;
    if (baseEntry.units.some((entry) => entry.status !== "passed")) throw new RunnerError("base package units did not complete");
    const coverageUnits = units.filter((unit) => unit.phase === "coverage");
    const mergePath = join(campaign.runDir, "profiles", "base", "coverage.out");
    mergeCoverProfiles(coverageUnits.map((unit) => packageUnitCoveragePath(campaign, entries.get(unit.id))), mergePath);
    const coverage = validateCoverage(mergePath);
    baseEntry.coverage = { path: relative(campaign.runDir, mergePath), ...coverage };
    baseEntry.status = "passed";
    baseEntry.completed_at_utc = new Date().toISOString();
    baseEntry.elapsed_ms = Date.now() - Date.parse(baseEntry.started_at_utc);
    return baseEntry;
  } catch (error) {
    const failure = classifyProfileFailure(error, execution);
    baseEntry.status = failure.status;
    baseEntry.failure_reason = failure.reason;
    baseEntry.failure = redacted(error.message, [process.env.SONAR_TOKEN, process.env.DATABASE_DSN, databasePassword]);
    baseEntry.completed_at_utc = new Date().toISOString();
    baseEntry.elapsed_ms = Date.now() - Date.parse(baseEntry.started_at_utc);
    baseEntry.unit_summary = terminalPackageSummary(baseEntry.units);
    throw error;
  } finally {
    saveCampaign(campaign);
  }
}

export async function collectCoverage(campaign, candidate, environment, options, execution, deadline, progress, runtime = {}) {
  const goCommand = runtime.goCommand ?? locate("go");
  const dockerCommand = runtime.dockerCommand ?? locate("docker");
  if (!goCommand) throw new RunnerError("Go is required to generate coverage");
  if (!dockerCommand) throw new RunnerError("Docker is required to generate isolated coverage");
  const imageId = environment.values.image_id;
  if (!/^sha256:[0-9a-f]{64}$/i.test(imageId)) throw new RunnerError("An immutable PostgreSQL image is required for coverage");
  const profiles = runtime.profiles ?? (options.baseOnly ? coverageProfiles.filter((profile) => profile.name === "base") : coverageProfiles);
  if (options.baseOnly && (profiles.length !== 1 || profiles[0]?.name !== "base")) throw new RunnerError("base-only coverage must select exactly the base profile");
  const base = profiles.find((profile) => profile.name === "base");
  const dedicated = profiles.filter((profile) => profile.name !== "base");
  const reusable = options.fresh ? new Map() : findReusableProfiles(campaign.namespace, dedicated, candidate, environment);
  for (const profile of profiles) {
    const cached = reusable.get(profile.name);
    const entry = {
      name: profile.name,
      descriptor: profile.name === "base" ? { kind: "package-units" } : profileDescriptor(profile),
      fingerprint: fingerprintProfile(profile, candidate, environment),
      status: "pending",
      attempt: 1,
      invalidation_reasons: cached ? [] : [options.fresh ? "fresh-requested" : "no-exact-valid-attempt"],
      resource_group: profile.resourceGroup,
      execution_environment_sha256: environment.sha256,
    };
    if (cached) {
      entry.status = "passed";
      entry.source_run_id = cached.sourceRun;
      entry.source_artifact = { path: cached.entry.coverage.path, sha256: cached.artifact.sha256, bytes: cached.artifact.bytes, coverage_classification: cached.artifact.coverage_classification };
      entry.coverage = { path: cached.entry.coverage.path, sha256: cached.artifact.sha256, bytes: cached.artifact.bytes, coverage_classification: cached.artifact.coverage_classification };
      entry.test_events = cached.entry.test_events;
      entry.expected_tests = cached.entry.expected_tests;
      entry.expected_test_inventory_sha256 = cached.entry.expected_test_inventory_sha256;
      entry.package_counts = cached.entry.package_counts;
      entry.allowed_skip_obligations = cached.entry.allowed_skip_obligations;
      entry.unexpected_skip_count = 0;
      event(campaign, "coverage", "profile_reused", { profile: profile.name, source_run_id: cached.sourceRun });
    }
    campaign.manifest.profiles.push(entry);
  }
  saveCampaign(campaign);
  let postgres = null;
  let postgresPromise = null;
  const secrets = [process.env.SONAR_TOKEN, process.env.DATABASE_DSN, databasePassword];
  const getPostgres = async () => {
    if (!postgresPromise) postgresPromise = startPostgres(dockerCommand, candidate.repository_path, imageId, campaign, execution, deadline).then((value) => (postgres = value));
    return postgresPromise;
  };
  if (!base) {
    progress.configureWork(coverageWorkPlan([], dedicated.length));
    for (const entry of campaign.manifest.profiles.filter((entry) => entry.source_run_id)) progress.complete("dedicated", { reused: true });
  }
  const entries = new Map(campaign.manifest.profiles.map((entry) => [entry.name, entry]));
  let primaryError = null;
  try {
    if (base) await runBasePackageUnits(entries.get("base"), campaign, candidate, environment, options, execution, deadline, progress, goCommand, runtime);
    if (options.baseOnly) {
      campaign.manifest.merged = { status: "incomplete", reason: "base_only", selected_profiles: 1, required_profiles: coverageProfiles.length, selected_units: entries.get("base").unit_plan?.selected_units || 0 };
      campaign.manifest.result.coverage = "incomplete";
      campaign.manifest.result.disposition = "base_profile_ready";
      progress.meaningful("coverage", { state: "base_profile_ready", selected_profiles: 1, required_profiles: coverageProfiles.length, selected_units: entries.get("base").unit_plan?.selected_units || 0 });
      saveCampaign(campaign);
      return;
    }
    const outcomes = await scheduleProfiles(dedicated.filter((profile) => !reusable.has(profile.name)), options.jobs, async (profile) => {
      const profileDeadline = Date.now() + deadline.profile;
      const database = profile.databasePrefix ? await getPostgres() : null;
      await runCoverageProfile(goCommand, profile, campaign, entries.get(profile.name), candidate.repository_path, environment.testEnvironment, database, execution, deadline, progress, secrets, profileDeadline, candidate);
      return { status: "passed" };
    });
    const failed = [...outcomes.values()].find((outcome) => outcome?.status === "failed");
    if (failed) throw failed.error;
    if (base) assertUnitDedicatedOwnership(entries.get("base").units, { runDir: campaign.runDir, manifest: campaign.manifest }, candidate, environment);
    const mergePath = join(campaign.runDir, "coverage.out");
    mergeCoverProfiles(profiles.map((profile) => {
      const entry = entries.get(profile.name);
      if (entry.source_run_id) {
        const source = manifestsIn(campaign.namespace).find((record) => record.manifest.run_id === entry.source_run_id);
        return artifactFrom(source, entry.source_artifact, { allowZeroCoverableStatements: entry.source_artifact?.coverage_classification === "zero_coverable_statements" }).path;
      }
      return safeRelative(campaign.runDir, entry.coverage.path);
    }), mergePath);
    const coverage = validateCoverage(mergePath);
    campaign.manifest.merged = { status: "passed", path: relative(campaign.runDir, mergePath), ...coverage, profile_digests: profiles.map((profile) => entries.get(profile.name).coverage.sha256) };
    campaign.manifest.result.coverage = "passed";
    progress.meaningful("coverage", { state: "complete", coverage_sha256: coverage.sha256 });
    saveCampaign(campaign);
  } catch (error) {
    primaryError = error;
    campaign.manifest.merged = { status: "incomplete" };
    campaign.manifest.result.coverage = "failed";
    campaign.manifest.result.failure = redacted(error.message, secrets);
    saveCampaign(campaign);
    throw error;
  } finally {
    if (postgres) {
      try { await postgres.stop(); } catch (error) {
        campaign.manifest.resources.cleanup.errors.push(redacted(error.message, secrets));
        if (!primaryError) throw error;
      }
    }
  }
}

function parseReport(path) {
  if (!existsSync(path)) throw new RunnerError(`SonarScanner did not write ${path}`);
  const values = new Map();
  for (const line of readFileSync(path, "utf8").split(/\r?\n/)) {
    if (!line.trim()) continue;
    const separator = line.indexOf("=");
    if (separator < 1) throw new RunnerError(`Malformed report-task line: ${line}`);
    const key = line.slice(0, separator).trim();
    if (!key || values.has(key)) throw new RunnerError(`Malformed report-task key: ${line}`);
    values.set(key, line.slice(separator + 1).trim());
  }
  for (const key of ["ceTaskId", "ceTaskUrl", "dashboardUrl", "projectKey", "serverUrl"]) {
    if (!values.get(key)) throw new RunnerError(`report-task.txt is missing ${key}`);
  }
  return values;
}

export function validateReport(report, sonarHost) {
  const configured = new URL(sonarHost);
  const server = new URL(report.get("serverUrl"));
  const taskUrl = new URL(report.get("ceTaskUrl"));
  if (
    configured.username || configured.password || configured.pathname !== "/" || configured.search || configured.hash ||
    server.username || server.password || server.href !== configured.href ||
    taskUrl.username || taskUrl.password || taskUrl.origin !== configured.origin || taskUrl.pathname !== "/api/ce/task" || taskUrl.hash ||
    taskUrl.searchParams.size !== 1 || taskUrl.searchParams.get("id") !== report.get("ceTaskId") ||
    report.get("projectKey") !== projectKey || !/^[\w-]+$/.test(report.get("ceTaskId"))
  ) throw new RunnerError("report-task.txt does not bind to the intended Sonar server/project/task");
  const dashboard = new URL(report.get("dashboardUrl"));
  if (dashboard.origin !== configured.origin || dashboard.username || dashboard.password || dashboard.hash || dashboard.pathname !== "/dashboard" || dashboard.searchParams.size !== 1 || dashboard.searchParams.get("id") !== projectKey) throw new RunnerError("report-task.txt has an unsafe dashboardUrl");
  return { ce_task_id: report.get("ceTaskId"), dashboard_url: report.get("dashboardUrl"), ce_task_url: report.get("ceTaskUrl") };
}

function readSonarDotEnv(path) {
  const values = new Map();
  for (const rawLine of readFileSync(path, "utf8").split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const match = /^(?:export\s+)?(SONAR_TOKEN|SONAR_HOST_URL)\s*=\s*(.*)$/.exec(line);
    if (!match) continue;
    if (values.has(match[1])) throw new RunnerError(`Duplicate ${match[1]} in ${path}`);
    let value = match[2].trim();
    if (value.length >= 2 && ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'")))) value = value.slice(1, -1);
    values.set(match[1], value);
  }
  return values;
}

function sonarCredentials(candidate) {
  const worktree = readSonarDotEnv(join(candidate.repository_path, ".env"));
  const shared = candidate.coordination_root === candidate.repository_path ? new Map() : readSonarDotEnv(join(candidate.coordination_root, ".env"));
  const token = process.env.SONAR_TOKEN?.trim() || worktree.get("SONAR_TOKEN")?.trim() || shared.get("SONAR_TOKEN")?.trim();
  const host = process.env.SONAR_HOST_URL?.trim() || worktree.get("SONAR_HOST_URL")?.trim() || shared.get("SONAR_HOST_URL")?.trim();
  if (!token) throw new RunnerError("SONAR_TOKEN is required in the process environment or project .env");
  if (!host) throw new RunnerError("SONAR_HOST_URL is required in the process environment or project .env");
  const parsed = new URL(host);
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || parsed.search || parsed.hash || !["", "/"].includes(parsed.pathname)) {
    throw new RunnerError("SONAR_HOST_URL must be a credential-free server base URL");
  }
  return { token, host: parsed.origin };
}

async function fetchJson(host, token, pathname, deadline, execution) {
  const url = new URL(pathname, host);
  const signal = AbortSignal.any([execution.signal, AbortSignal.timeout(Math.min(30000, deadline.timeoutFor("quality", 30000)))]);
  let response;
  try {
    response = await fetch(url, { headers: { Authorization: `Bearer ${token}` }, redirect: "error", signal });
  } catch (error) {
    if (execution.signal.aborted) throw execution.signal.reason;
    throw new RunnerError(`Sonar API request failed: ${error.message}`, 1, true);
  }
  const text = await response.text();
  if (response.status === 401 || response.status === 403) throw new RunnerError(`Sonar API authorization failed (${response.status})`);
  if (response.status >= 500) throw new RunnerError(`Sonar API transient failure (${response.status})`, 1, true);
  if (!response.ok) throw new RunnerError(`Sonar API failure (${response.status})`);
  try { return JSON.parse(text); } catch { throw new RunnerError("Sonar API returned invalid JSON"); }
}

function analysisPath(campaign, file) {
  return join(campaign.runDir, "analysis", file);
}

function saveAnalysisJson(campaign, file, value) {
  const path = analysisPath(campaign, file);
  writeJson(path, value);
  return { path: relative(campaign.runDir, path), sha256: shaFile(path), bytes: statSync(path).size };
}

export function materializeCoverage(campaign, candidate) {
  const sourceRun = campaign.manifest.merged.source_run_id;
  const sourceRecord = sourceRun
    ? referencedRun({ runDir: campaign.runDir }, sourceRun, campaign.manifest.candidate, { sha256: campaign.manifest.fingerprints.coverage_environment })
    : { runDir: campaign.runDir, manifest: campaign.manifest };
  if (!sourceRecord) throw new RunnerError("Retained coverage source run is invalid");
  const source = safeRelative(sourceRecord.runDir, campaign.manifest.merged.path);
  const destination = join(candidate.repository_path, "coverage.out");
  validateCoverage(source, campaign.manifest.merged.sha256);
  const temporary = `${destination}.${campaign.manifest.run_id}.tmp`;
  copyFileSync(source, temporary);
  renameSync(temporary, destination);
  return destination;
}

async function scannerSubmit(campaign, candidate, credentials, options, execution, deadline, progress) {
  const scanner = resolveScanner(options.scannerCommand);
  if (!scanner) throw new RunnerError(`SonarScanner was not found: ${options.scannerCommand}`);
  await assertCandidate(candidate, execution, { requireClean: true });
  const materializedCoverage = materializeCoverage(campaign, candidate);
  const reportPath = analysisPath(campaign, "report-task.txt");
  const scannerLogPath = analysisPath(campaign, "scanner.log");
  const attemptId = randomUUID();
  campaign.manifest.analysis = {
    ...campaign.manifest.analysis,
    state: "submitting",
    attempt_id: attemptId,
    host: credentials.host,
    project_key: projectKey,
    scm_revision: candidate.head,
    build_string: `${candidate.head}:${attemptId}`,
    report_path: relative(campaign.runDir, reportPath),
    submitted_at_utc: new Date().toISOString(),
  };
  saveCampaign(campaign);
  event(campaign, "scanner", "submission_started", { attempt_id: attemptId });
  const log = createLogWriter(scannerLogPath, [credentials.token]);
  try {
    try {
      await runProcess(scanner, [
        `-Dsonar.host.url=${credentials.host}`,
        `-Dsonar.scm.revision=${candidate.head}`,
        `-Dsonar.buildString=${candidate.head}:${attemptId}`,
        "-Dsonar.qualitygate.wait=false",
        `-Dsonar.scanner.metadataFilePath=${reportPath}`,
      ], {
        ...execution,
        cwd: candidate.repository_path,
        env: { ...scrubTestEnvironment(), SONAR_TOKEN: credentials.token },
        timeoutMs: deadline.timeoutFor("scanner", deadline.limits.scanner),
        label: "SonarScanner submission",
        onStdout: (chunk) => log.write(chunk),
        onStderr: (chunk) => log.write(chunk),
      });
    } catch (error) {
      log.finish();
      if (!existsSync(reportPath)) {
        campaign.manifest.analysis.state = "submission_unknown";
        campaign.manifest.analysis.failure = redacted(error.message, [credentials.token]);
        campaign.manifest.analysis.scanner_log = { path: relative(campaign.runDir, scannerLogPath), sha256: shaFile(scannerLogPath), bytes: statSync(scannerLogPath).size };
        saveCampaign(campaign);
        throw new RunnerError("SonarScanner failed before a durable CE task ID; automatic resubmission is unsafe");
      }
    }
    log.finish();
    const report = validateReport(parseReport(reportPath), credentials.host);
    campaign.manifest.analysis = {
      ...campaign.manifest.analysis,
      ...report,
      scanner_identity: { path: scanner, sha256: shaFile(scanner) },
      state: "submitted",
      report: { path: relative(campaign.runDir, reportPath), sha256: shaFile(reportPath), bytes: statSync(reportPath).size },
      scanner_log: { path: relative(campaign.runDir, scannerLogPath), sha256: shaFile(scannerLogPath), bytes: statSync(scannerLogPath).size },
    };
    campaign.manifest.fingerprints.analysis_inputs = sha256(canonicalJson({
      candidate: candidate.inputs_sha256,
      sonar_project: shaFile(join(candidate.repository_path, "sonar-project.properties")),
      scanner: campaign.manifest.analysis.scanner_identity,
    }));
    saveCampaign(campaign);
    progress.meaningful("scanner", { state: "submitted", ce_task_id: report.ce_task_id });
  } finally {
    log.finish();
    rmSync(materializedCoverage, { force: true });
  }
}

export async function waitForAnalysis(campaign, candidate, credentials, execution, deadline, progress) {
  const analysis = campaign.manifest.analysis;
  if (!analysis?.ce_task_id || !["submitted", "ce_running"].includes(analysis.state)) throw new RunnerError("Campaign has no resumable submitted CE task");
  if (analysis.host !== credentials.host || analysis.project_key !== projectKey || analysis.scm_revision !== candidate.head) {
    throw new RunnerError("Saved Sonar task does not bind to the current exact candidate/server");
  }
  const attempt = { started_at_utc: new Date().toISOString(), state: "waiting" };
  analysis.attempts ||= [];
  analysis.attempts.push(attempt);
  analysis.state = "ce_running";
  saveCampaign(campaign);
  let lastStatus = null;
  while (true) {
    let task;
    try {
      task = await fetchJson(credentials.host, credentials.token, `/api/ce/task?id=${encodeURIComponent(analysis.ce_task_id)}`, deadline, execution);
    } catch (error) {
      if (error instanceof SignalError || error instanceof BudgetError || !error.retryable) throw error;
      event(campaign, "ce", "transient_failure", { message: redacted(error.message, [credentials.token]) });
      await wait(5000, execution.signal);
      continue;
    }
    const current = task.task;
    if (!current || current.id !== analysis.ce_task_id || current.componentKey !== projectKey) throw new RunnerError("Sonar CE response did not match the saved task/project");
    analysis.last_ce_status = current.status;
    analysis.ce = saveAnalysisJson(campaign, "ce.json", task);
    if (current.status !== lastStatus) {
      lastStatus = current.status;
      progress.meaningful("ce", { ce_status: current.status, ce_task_id: analysis.ce_task_id });
    }
    if (["PENDING", "IN_PROGRESS"].includes(current.status)) {
      saveCampaign(campaign);
      await wait(5000, execution.signal);
      continue;
    }
    if (current.status !== "SUCCESS" || !current.analysisId) {
      analysis.state = "ce_failed";
      attempt.state = "failed";
      attempt.completed_at_utc = new Date().toISOString();
      campaign.manifest.result.technical_gate = "failed";
      campaign.manifest.result.disposition = "failed";
      saveCampaign(campaign);
      throw new RunnerError(`Sonar CE task ended ${current.status}`);
    }
    analysis.analysis_id = current.analysisId;
    const gate = await fetchJson(credentials.host, credentials.token, `/api/qualitygates/project_status?analysisId=${encodeURIComponent(current.analysisId)}`, deadline, execution);
    analysis.quality_gate = saveAnalysisJson(campaign, "quality-gate.json", gate);
    if (gate?.projectStatus?.status !== "OK") {
      analysis.state = "gate_failed";
      attempt.state = "gate_failed";
      attempt.completed_at_utc = new Date().toISOString();
      campaign.manifest.result.technical_gate = "failed";
      campaign.manifest.result.disposition = "failed";
      saveCampaign(campaign);
      throw new RunnerError(`QUALITY GATE STATUS: ${gate?.projectStatus?.status || "unknown"}`);
    }
    analysis.state = "passed";
    attempt.state = "passed";
    attempt.completed_at_utc = new Date().toISOString();
    campaign.manifest.result.technical_gate = "passed";
    campaign.manifest.result.disposition = "passed";
    saveCampaign(campaign);
    progress.meaningful("quality-gate", { state: "OK", analysis_id: current.analysisId });
    return;
  }
}

export async function executeAnalysis(analysis, { submit, wait: waitForSubmitted, resumeOnly = false }) {
  if (analysis.state === "submission_unknown") {
    throw new RunnerError("Submission is ambiguous; automatic scanner resubmission is unsafe");
  }
  if (["submitted", "ce_running"].includes(analysis.state)) {
    await waitForSubmitted();
    return "resumed";
  }
  if (analysis.state === "passed") return "completed";
  if (analysis.state !== "not_submitted") throw new RunnerError(`Analysis cannot be resumed from ${analysis.state}`);
  if (resumeOnly) throw new RunnerError("Resume requires a durable submitted CE task and never submits a new analysis");
  await submit();
  await waitForSubmitted();
  return "submitted";
}

function createStatusPublication(candidate) {
  const gh = locate("gh");
  if (!gh) throw new RunnerError("GitHub CLI is required for --publish-status");
  return { gh, candidate };
}

async function publishStatus(publication, state, targetUrl, execution) {
  await capture(publication.gh, [
    "api", "--method", "POST", `repos/${await capture(publication.gh, ["repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"], publication.candidate.repository_path, execution)}/statuses/${publication.candidate.head}`,
    "--raw-field", `state=${state}`,
    "--raw-field", "context=SonarQube Quality Gate",
    "--raw-field", `description=${state === "success" ? "SonarQube Quality Gate passed" : state === "pending" ? "SonarQube Quality Gate is running" : "SonarQube Quality Gate failed"}`,
    ...(targetUrl ? ["--raw-field", `target_url=${targetUrl}`] : []),
  ], publication.candidate.repository_path, execution);
}

function legacyReceiptPath(candidate) {
  return join(candidate.coordination_root, ".agent", "e", "sonarqube", `${candidate.head}.json`);
}

export function writeReceipt(campaign, candidate, credentials) {
  const cleanup = campaign.manifest.resources?.cleanup;
  const container = campaign.manifest.resources?.container;
  if (cleanup?.state !== "complete" || cleanup.errors?.length || (container && container.state !== "removed")) {
    throw new RunnerError("Refusing PASS receipt while runner-owned cleanup is unresolved");
  }
  if (campaign.manifest.result?.technical_gate !== "passed" || campaign.manifest.analysis?.state !== "passed" || !campaign.manifest.analysis?.analysis_id) {
    throw new RunnerError("Refusing PASS receipt without an exact Quality Gate result");
  }
  if (campaign.manifest.publication?.requested && campaign.manifest.result?.effect !== "success") {
    throw new RunnerError("Refusing PASS receipt while requested status publication failed");
  }
  const receipt = {
    schema_version: 2,
    gate: "sonarqube",
    project_key: projectKey,
    sonar_host_url: credentials.host,
    verdict: "PASS",
    head: candidate.head,
    completed_at_utc: new Date().toISOString(),
    ce_task_url: campaign.manifest.analysis.ce_task_url,
    dashboard_url: campaign.manifest.analysis.dashboard_url,
    manifest_path: join(campaign.runDir, "manifest.json"),
    manifest_sha256: shaFile(join(campaign.runDir, "manifest.json")),
    analysis_id: campaign.manifest.analysis.analysis_id,
    coverage_sha256: campaign.manifest.merged.sha256,
    fingerprint: campaign.manifest.fingerprints.analysis_inputs,
  };
  atomicWrite(legacyReceiptPath(candidate), `${JSON.stringify(receipt, null, 2)}\n`);
  return legacyReceiptPath(candidate);
}

async function cleanupCampaign(campaign, children) {
  const cleanup = campaign.manifest.resources.cleanup;
  try {
    await terminateOwnedChildren(children);
    if (children.size) throw new RunnerError("Runner-owned children remain after cleanup");
    const container = campaign.manifest.resources.container;
    if (container && container.state !== "removed") throw new RunnerError("Runner-owned PostgreSQL container remains after cleanup");
    if (cleanup.errors.length) throw new RunnerError("Runner-owned cleanup recorded errors");
    cleanup.state = "complete";
  } catch (error) {
    cleanup.state = "failed";
    cleanup.errors.push(error.message);
  }
  campaign.manifest.resources.children = children.history || [];
  saveCampaign(campaign);
}

function campaignFromRecord(record) {
  return { runDir: record.runDir, manifest: record.manifest, runId: record.manifest.run_id };
}

export async function runWithCampaign(campaign, candidate, environment, options, execution, deadline, progress, runtime = {}) {
  if (options.mode === "coverage" || options.mode === "gate") {
    if (campaign.manifest.result.coverage !== "passed") await collectCoverage(campaign, candidate, environment, options, execution, deadline, progress, runtime.coverage);
    if (options.mode === "coverage") {
      if (options.baseOnly) {
        console.log(`BASE_PROFILE_READY ${campaign.runId}`);
        return;
      }
      campaign.manifest.result.disposition = "coverage_ready";
      saveCampaign(campaign);
      console.log(`COVERAGE_READY ${campaign.runId}`);
      return;
    }
  }
  if (options.mode === "scan") {
    const source = findCompleteCoverage(campaign.namespace, candidate, environment);
    if (!source) throw new RunnerError("No complete exact-worktree coverage manifest is available for scan mode");
    campaign.manifest.profiles = source.manifest.profiles;
    campaign.manifest.merged = { ...source.manifest.merged, source_run_id: source.manifest.merged.source_run_id || source.manifest.run_id };
    campaign.manifest.result.coverage = "passed";
    campaign.manifest.coverage_source_run_id = campaign.manifest.merged.source_run_id;
    saveCampaign(campaign);
  }
  const credentials = sonarCredentials(candidate);
  const publicationRequested = options.publishStatus || campaign.manifest.publication?.requested;
  campaign.manifest.publication.requested = Boolean(publicationRequested);
  let publication = null;
  if (publicationRequested) {
    publication = createStatusPublication(candidate);
    try {
      await publishStatus(publication, "pending", null, execution);
      campaign.manifest.publication.state = "pending";
      campaign.manifest.result.effect = "pending";
    } catch (error) {
      campaign.manifest.publication.state = "failed";
      campaign.manifest.publication.error = error.message;
      campaign.manifest.result.effect = "failed";
    }
    saveCampaign(campaign);
  }
  await executeAnalysis(campaign.manifest.analysis, {
    submit: () => scannerSubmit(campaign, candidate, credentials, options, execution, deadline, progress),
    wait: () => waitForAnalysis(campaign, candidate, credentials, execution, deadline, progress),
    resumeOnly: options.mode === "resume",
  });
  try {
    await assertCandidate(candidate, execution, { requireClean: true });
  } catch (error) {
    campaign.manifest.result.disposition = "failed";
    campaign.manifest.result.failure = redacted(error.message, [credentials.token]);
    if (publication) {
      try {
        await publishStatus(publication, "failure", campaign.manifest.analysis.dashboard_url, execution);
        campaign.manifest.publication.failure_status_published = true;
      } catch (statusError) {
        campaign.manifest.publication.failure_status_error = redacted(statusError.message, [credentials.token]);
      }
    }
    saveCampaign(campaign);
    throw error;
  }
  await cleanupCampaign(campaign, execution.children);
  saveCampaign(campaign);
  if (campaign.manifest.resources.cleanup.state !== "complete") {
    if (publication) {
      try {
        await publishStatus(publication, "failure", campaign.manifest.analysis.dashboard_url, execution);
        campaign.manifest.publication.failure_status_published = true;
      } catch (statusError) {
        campaign.manifest.publication.failure_status_error = redacted(statusError.message, [credentials.token]);
      }
    }
    saveCampaign(campaign);
    throw new RunnerError("Runner-owned cleanup failed after technical Quality Gate success");
  }
  if (publication) {
    try {
      await publishStatus(publication, "success", campaign.manifest.analysis.dashboard_url, execution);
      campaign.manifest.publication.state = "success";
      campaign.manifest.result.effect = "success";
    } catch (error) {
      campaign.manifest.publication.state = "failed";
      campaign.manifest.publication.error = error.message;
      campaign.manifest.result.effect = "failed";
      campaign.manifest.result.disposition = "failed";
      await cleanupCampaign(campaign, execution.children);
      saveCampaign(campaign);
      throw new RunnerError(`Technical Quality Gate passed but commit-status publication failed: ${error.message}`);
    }
  }
  await cleanupCampaign(campaign, execution.children);
  saveCampaign(campaign);
  console.log(writeReceipt(campaign, candidate, credentials));
}
export async function main(options = parseOptions(process.argv.slice(2))) {
  const children = new Map();
  children.history = [];
  const controller = new AbortController();
  const onSignal = (signal) => () => controller.abort(new SignalError(signal));
  const onInterrupt = onSignal("SIGINT");
  const onTerminate = onSignal("SIGTERM");
  process.once("SIGINT", onInterrupt);
  process.once("SIGTERM", onTerminate);
  let campaign = null;
  let releaseLock = null;
  let progress = null;
  let candidate = null;
  const deadline = new Deadline(options);
  const preflightStarted = Date.now();
  const preflightHeartbeat = setInterval(() => {
    console.log(`sonar preflight: elapsed_ms=${Date.now() - preflightStarted} deadline_remaining_ms=${Math.max(0, deadline.overall - Date.now() - cleanupReserveSeconds * 1000)}`);
  }, 10000);
  preflightHeartbeat.unref?.();
  try {
    const preliminaryExecution = { children, signal: controller.signal, deadline };
    const repoRoot = resolve(await capture("git", ["rev-parse", "--show-toplevel"], options.repository, preliminaryExecution));
    if (!existsSync(join(repoRoot, "sonar-project.properties"))) throw new RunnerError(`Repository has no sonar-project.properties: ${repoRoot}`);
    candidate = await candidateFor(repoRoot, preliminaryExecution);
    if ((options.mode === "gate" || options.mode === "coverage") && await capture("git", ["status", "--porcelain"], candidate.repository_path, preliminaryExecution)) {
      throw new RunnerError("Refusing to run SonarQube against a dirty working tree");
    }
    const namespace = join(candidate.coordination_root, ".agent", "e", "sonarqube", "worktrees", candidate.worktree_id);
    const dockerCommand = locate("docker");
    const existingImage = dockerCommand ? await bestEffortCapture(dockerCommand, ["image", "inspect", "--format", "{{.Id}}", imageReference], repoRoot, preliminaryExecution) : "unavailable";
    const imageId = /^sha256:[0-9a-f]{64}$/i.test(existingImage) ? existingImage : (options.mode === "coverage" || options.mode === "gate") && dockerCommand ? await resolveImage(dockerCommand, repoRoot, preliminaryExecution) : "unavailable";
    const environment = await coverageEnvironment(repoRoot, { goCommand: locate("go"), dockerCommand, imageId, execution: preliminaryExecution });
    if (options.mode === "resume") {
      const record = manifestsIn(namespace).find((item) => item.manifest.run_id === options.run);
      if (!record) throw new RunnerError(`No campaign found for --run ${options.run}`);
      if (!sameCandidate(record.manifest.candidate, candidate) || record.manifest.fingerprints.coverage_environment !== environment.sha256) {
        throw new RunnerError("Resume campaign no longer matches the exact candidate/environment");
      }
      if (!["submitted", "ce_running"].includes(record.manifest.analysis?.state) || !completeCoverage(record, candidate, environment) || !validAnalysisEvidence(record)) {
        throw new RunnerError("Resume requires complete retained coverage and a validated submitted CE task");
      }
      campaign = campaignFromRecord(record);
      campaign.namespace = namespace;
    } else {
      if ((options.mode === "gate" || options.mode === "scan") && !options.fresh) {
        const pending = resumableAnalysis(namespace, candidate, environment);
        if (pending) {
          campaign = campaignFromRecord(pending);
          campaign.namespace = namespace;
        }
      }
      if (!campaign) {
        campaign = createCampaign(namespace, options.mode, candidate, environment, options);
        campaign.namespace = namespace;
      }
    }
    releaseLock = acquireLock(namespace, campaign.runId);
    progress = new Progress(campaign, deadline);
    progress.start();
    clearInterval(preflightHeartbeat);
    const selectedProfiles = options.baseOnly ? 1 : coverageProfiles.length;
    event(campaign, "preflight", "started", { mode: options.mode, head: candidate.head, selected_profiles: selectedProfiles, required_profiles: coverageProfiles.length, jobs: options.jobs, manifest_path: join(campaign.runDir, "manifest.json"), budgets: campaign.manifest.budgets });
    console.log(`Sonar mode=${options.mode} head=${candidate.head} profiles=${selectedProfiles}/${coverageProfiles.length} jobs=${options.jobs} manifest=${campaign.runDir}`);
    await assertCandidate(candidate, preliminaryExecution, { requireClean: options.mode === "gate" || options.mode === "coverage" });
    await runWithCampaign(campaign, candidate, environment, options, preliminaryExecution, deadline, progress);
  } catch (error) {
    if (campaign && campaign.manifest.result.technical_gate !== "passed") {
      campaign.manifest.result.disposition = error instanceof SignalError || error instanceof BudgetError ? "incomplete" : "failed";
      campaign.manifest.result.failure = redacted(error.message, [process.env.SONAR_TOKEN]);
      if (campaign.manifest.publication?.requested && candidate) {
        try {
          await publishStatus(createStatusPublication(candidate), "failure", campaign.manifest.analysis?.dashboard_url || null, cleanupExecution({ children, signal: controller.signal }));
          campaign.manifest.publication.failure_status_published = true;
        } catch (statusError) {
          campaign.manifest.publication.failure_status_error = redacted(statusError.message, [process.env.SONAR_TOKEN]);
        }
      }
      saveCampaign(campaign);
    }
    throw error;
  }
  finally {
    clearInterval(preflightHeartbeat);
    progress?.stop();
    if (campaign) await cleanupCampaign(campaign, children);
    releaseLock?.();
    process.removeListener("SIGINT", onInterrupt);
    process.removeListener("SIGTERM", onTerminate);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  try {
    await main();
  } catch (error) {
    console.error(error.stack || error.message);
    process.exitCode = error instanceof RunnerError ? error.exitCode : 1;
  }
}
