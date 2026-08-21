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
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptRoot = dirname(fileURLToPath(import.meta.url));
let repository = resolve(scriptRoot, "../..");
let scannerCommand = "sonar-scanner";
let qualityGateTimeout = 600;

for (let index = 2; index < process.argv.length; index += 1) {
  const option = process.argv[index];
  const value = process.argv[index + 1];
  if (!value) throw new Error(`Missing value for ${option}`);
  if (option === "--repository") repository = resolve(value);
  else if (option === "--scanner") scannerCommand = value;
  else if (option === "--timeout") {
    if (!/^[1-9]\d*$/.test(value)) throw new Error("--timeout must be a positive integer");
    qualityGateTimeout = Number(value);
    if (!Number.isSafeInteger(qualityGateTimeout)) {
      throw new Error("--timeout exceeds the safe integer range");
    }
  } else throw new Error(`Unknown option: ${option}`);
  index += 1;
}

function capture(command, args, cwd) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    windowsHide: true,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(
      `${command} failed with exit code ${result.status}: ${(result.stderr || result.stdout).trim()}`,
    );
  }
  return result.stdout.trim();
}

function run(command, args, cwd) {
  const isWindowsBatch = process.platform === "win32" && /\.(?:bat|cmd)$/i.test(command);
  const executable = isWindowsBatch ? process.env.ComSpec || "cmd.exe" : command;
  const executableArgs = isWindowsBatch ? ["/d", "/c", command, ...args] : args;
  const result = spawnSync(executable, executableArgs, {
    cwd,
    env: process.env,
    stdio: "inherit",
    windowsHide: true,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`${command} failed with exit code ${result.status}`);
  }
}

function locate(command) {
  if (existsSync(command)) return resolve(command);
  const locator = process.platform === "win32" ? "where.exe" : "which";
  const result = spawnSync(locator, [command], {
    encoding: "utf8",
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

const repoRoot = resolve(capture("git", ["rev-parse", "--show-toplevel"], repository));
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
const reportTaskPath = join(repoRoot, ".scannerwork", "report-task.txt");
mkdirSync(receiptDirectory, { recursive: true });
rmSync(receiptPath, { force: true });
rmSync(reportTaskPath, { force: true });

if (capture("git", ["status", "--porcelain"], repoRoot)) {
  throw new Error("Refusing to run SonarQube against a dirty working tree");
}
const worktreeDotEnv = readSonarDotEnv(join(repoRoot, ".env"));
const sharedDotEnv =
  coordinationRoot === repoRoot ? new Map() : readSonarDotEnv(join(coordinationRoot, ".env"));
const sonarToken =
  process.env.SONAR_TOKEN?.trim() ||
  worktreeDotEnv.get("SONAR_TOKEN")?.trim() ||
  sharedDotEnv.get("SONAR_TOKEN")?.trim();
if (!sonarToken) throw new Error("SONAR_TOKEN is required in the process environment or project .env");
process.env.SONAR_TOKEN = sonarToken;
const sonarHostUrl =
  process.env.SONAR_HOST_URL?.trim() ||
  worktreeDotEnv.get("SONAR_HOST_URL")?.trim() ||
  sharedDotEnv.get("SONAR_HOST_URL")?.trim() ||
  "http://unleashed.lan:9000";
const parsedHost = new URL(sonarHostUrl);
if (!["http:", "https:"].includes(parsedHost.protocol)) {
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
let scannerPath = locate(scannerCommand);
if (!scannerPath && process.platform === "win32" && scannerCommand === "sonar-scanner") {
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
if (!scannerPath) throw new Error(`SonarScanner was not found: ${scannerCommand}`);

run(
  goCommand,
  ["test", "./...", "-race", "-covermode=atomic", "-coverprofile=coverage.out"],
  repoRoot,
);
run(
  scannerPath,
  [
    `-Dsonar.host.url=${sonarHostUrl}`,
    `-Dsonar.scm.revision=${head}`,
    `-Dsonar.buildString=${head}`,
    "-Dsonar.qualitygate.wait=true",
    `-Dsonar.qualitygate.timeout=${qualityGateTimeout}`,
  ],
  repoRoot,
);

const finalHead = capture("git", ["rev-parse", "--verify", "HEAD^{commit}"], repoRoot).toLowerCase();
if (finalHead !== head) throw new Error(`Git HEAD changed during analysis: ${head} -> ${finalHead}`);
if (capture("git", ["status", "--porcelain"], repoRoot)) {
  throw new Error("Working tree changed during SonarQube analysis");
}

const report = parseReport(reportTaskPath);
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
if (receiptJson.includes(sonarToken)) throw new Error("Receipt contains SONAR_TOKEN");

const temporaryReceipt = join(receiptDirectory, `.${head}.${randomUUID()}.tmp`);
try {
  writeFileSync(temporaryReceipt, receiptJson, { encoding: "utf8", mode: 0o600 });
  renameSync(temporaryReceipt, receiptPath);
} finally {
  rmSync(temporaryReceipt, { force: true });
}
console.log(receiptPath);
