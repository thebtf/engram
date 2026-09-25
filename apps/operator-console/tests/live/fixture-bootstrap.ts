import { spawn } from 'node:child_process'
import type { ChildProcess } from 'node:child_process'
import { randomBytes, randomUUID, createHash } from 'node:crypto'
import { access, lstat, mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { constants } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const READY_TIMEOUT_MS = 60_000
const PROCESS_STOP_TIMEOUT_MS = 10_000
const POSTGRES_IMAGE = 'pgvector/pgvector:pg17'
const FIXTURE_OVERRIDDEN_ENVIRONMENT_NAMES: Record<string, true> = {
  DATABASE_DSN: true,
  DATABASE_URL: true,
  ENGRAM_AUTH_ADMIN_TOKEN: true,
  ENGRAM_AUTH_DISABLED: true,
  ENGRAM_CODE_INTEL_ENABLED: true,
  ENGRAM_VNEXT_F_ENABLED: true,
  ENGRAM_TOKEN: true,
  ENGRAM_WORKSTATION_TOKEN: true,
  ENGRAM_API_TOKEN: true,
  ENGRAM_API_KEY: true,
  ENGRAM_SERVER_URL: true,
  ENGRAM_URL: true,
  API_TOKEN: true,
}
const FORBIDDEN_MOCK_COMMAND = 'scripts/mock-operator-api.mjs'
const STATE_SCHEMA = 'operator-console-live-fixture/v1'

const liveDir = dirname(fileURLToPath(import.meta.url))
const consoleRoot = resolve(liveDir, '../..')
const repositoryRoot = resolve(consoleRoot, '../..')

type TrafficOrigin = 'bootstrap' | 'browser'

export interface RouteTraffic {
  origin: TrafficOrigin
  method: string
  path: string
  status: number
  at: string
}

export interface LiveFixtureState {
  schema: typeof STATE_SCHEMA
  fixtureId: string
  candidate: {
    commit: string
    tree: string
  }
  backend: {
    baseUrl: string
    sourceCommit: string
    binarySha256: string
    ready: RouteTraffic
  }
  frontend: {
    baseUrl: string
    buildEntry: string
    buildEntrySha256: string
  }
  postgres: {
    container: string
    host: string
    port: number
    database: 'engram'
  }
  browserCredential: {
    email: string
    password: string
  }
  browserCredentialB: {
    email: string
    password: string
  }
  mock: {
    prohibited: true
    liveConfigContainsMockCommand: false
  }
  operatorCode: OperatorCodeFixture
  operatorCodeAlternate: OperatorCodeFixture
  operatorCodeB: OperatorCodeFixture
  operatorCodeFirstIndex: FirstIndexFixture
  worktrees: {
    a: FixtureWorktree
    b: FixtureWorktree
    c: FixtureWorktree
  }
  mcp: {
    clientBinary: string
    clientBinarySha256: string
    clientRoots: {
      a: string
      b: string
      c: string
    }
    registration: {
      clientRoot: string
      keycardFile: string
    }
    firstIndex: FirstIndexMCPFixture
  }
  traffic: RouteTraffic[]
}

interface FixtureWorktree {
  head: string
  tree: string
  fixtureSourceSha256: string
}

interface FixtureWorktreeRuntime {
  root: string
  sourceFile: string
  identity: FixtureWorktree
}

interface FixtureWorktreeSet {
  a: FixtureWorktreeRuntime
  b: FixtureWorktreeRuntime
  c: FixtureWorktreeRuntime
}

interface OperatorCodeFixture {
  query: string
  expectedSearch: string
  expectedGraph: string
  expectedSource: string
  expectedMarker: string
}

interface FirstIndexFixture extends OperatorCodeFixture {
  analysisProfileId: string
  checkoutId: string
  incarnationId: string
  parserBundleDigest: string
  sourceId: string
}

interface FirstIndexMCPFixture {
  clientRoot: string
  keycardFile: string
  parserBundleDigest: string
  parserExecutableSha256: string
  parserExecutable: string
}

interface CommandResult {
  stdout: string
  stderr: string
}

interface FixtureController {
  start(): Promise<void>
  stop(): Promise<void>
}

class LiveFixture implements FixtureController {
  private readonly fixtureId = `operator-live-${process.pid}-${randomUUID().replaceAll('-', '')}`
  private readonly containerName = this.fixtureId
  private readonly traffic: RouteTraffic[] = []
  private readonly browserPassword = `Live-${randomBytes(24).toString('base64url')}`
  private readonly browserPasswordB = `Live-${randomBytes(24).toString('base64url')}`
  private readonly adminToken = randomBytes(32).toString('base64url')
  private readonly projectAnchorID = randomUUID()
  private readonly browserEmail = `${this.fixtureId}@fixture.invalid`
  private readonly browserEmailB = `${this.fixtureId}-b@fixture.invalid`
  private fixtureRoot = ''
  private statePath = ''
  private server?: ChildProcess
  private console?: ChildProcess
  private postgresStarted = false

  async start(): Promise<void> {
    await assertLiveHarnessContract()
    await assertFile(join(consoleRoot, '.output', 'server', 'index.mjs'), 'built Nuxt output')

    this.fixtureRoot = await mkdtemp(join(tmpdir(), `${this.fixtureId}-`))
    this.statePath = join(this.fixtureRoot, 'fixture-state.json')
    await mkdir(join(this.fixtureRoot, 'home'), { recursive: true })


    const [commitResult, treeResult] = await Promise.all([
      execute('git', ['rev-parse', 'HEAD'], repositoryRoot),
      execute('git', ['rev-parse', 'HEAD^{tree}'], repositoryRoot),
    ])
    const candidate = {
      commit: commitResult.stdout.trim(),
      tree: treeResult.stdout.trim(),
    }

    try {
      const worktrees = await this.createWorktrees()
      const postgres = await this.startPostgres()
      const binary = await this.buildServer(candidate.commit)
      const clientBinary = await this.buildMCPClient()
      const parserBinary = await this.buildMCPParser()
      const { apiUrl, health, ready } = await this.startServer(binary, postgres.dsn)
      const healthBody = health.body
      if (
        health.status !== 200
        || healthBody === null
        || typeof healthBody !== 'object'
        || Array.isArray(healthBody)
        || !('status' in healthBody)
        || !('source_commit' in healthBody)
        || healthBody.status !== 'ready'
        || typeof healthBody.source_commit !== 'string'
        || healthBody.source_commit !== candidate.commit
      ) {
        throw new Error(`Go API candidate identity mismatch: ${health.status} ${JSON.stringify(healthBody)}`)
      }

      const setup = await this.fetchJSON(`${apiUrl}/api/auth/setup`, {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${this.adminToken}`,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ email: this.browserEmail, password: this.browserPassword }),
      })
      if (setup.status !== 201) {
        throw new Error(`real API fixture auth setup failed: ${setup.status} ${JSON.stringify(setup.body)}`)
      }
      const operatorCode = await this.provisionOperatorCode(postgres.dsn, worktrees.a, 'a', this.browserEmail)
      const operatorCodeAlternate = await this.provisionOperatorCode(postgres.dsn, worktrees.b, 'd', this.browserEmail)
      const operatorCodeFirstIndex = await this.provisionNoViewOperatorCode(postgres.dsn, worktrees.c, this.browserEmail)
      const operatorCodeB = await this.provisionOperatorCode(postgres.dsn, worktrees.b, 'b', this.browserEmailB, this.browserPasswordB)
      const appUrl = await this.startConsole(apiUrl)
      await this.awaitReady(`${appUrl}/settings`, this.console, 'built Nuxt console')
      const registrationKeycardFile = join(this.fixtureRoot, 'project-identity-registration.keycard')
      await writeFile(registrationKeycardFile, `${this.adminToken}\n`, { encoding: 'utf8', mode: 0o600 })

      const state: LiveFixtureState = {
        schema: STATE_SCHEMA,
        fixtureId: this.fixtureId,
        candidate,
        backend: {
          baseUrl: apiUrl,
          sourceCommit: healthBody.source_commit,
          binarySha256: createHash('sha256').update(await readFile(binary)).digest('hex'),
          ready,
        },
        frontend: {
          baseUrl: appUrl,
          buildEntry: relative(consoleRoot, join(consoleRoot, '.output', 'server', 'index.mjs')).replaceAll('\\', '/'),
          buildEntrySha256: createHash('sha256').update(await readFile(join(consoleRoot, '.output', 'server', 'index.mjs'))).digest('hex'),
        },
        postgres: {
          container: this.containerName,
          host: '127.0.0.1',
          port: postgres.port,
          database: 'engram',
        },
        browserCredential: {
          email: this.browserEmail,
          password: this.browserPassword,
        },
        browserCredentialB: {
          email: this.browserEmailB,
          password: this.browserPasswordB,
        },
        mock: {
          prohibited: true,
          liveConfigContainsMockCommand: false,
        },
        operatorCode,
        operatorCodeAlternate,
        operatorCodeB,
        operatorCodeFirstIndex: operatorCodeFirstIndex.fixture,
        worktrees: {
          a: worktrees.a.identity,
          b: worktrees.b.identity,
          c: worktrees.c.identity,
        },
        mcp: {
          clientBinary,
          clientBinarySha256: createHash('sha256').update(await readFile(clientBinary)).digest('hex'),
          clientRoots: { a: worktrees.a.root, b: worktrees.b.root, c: worktrees.c.root },
          firstIndex: {
            clientRoot: worktrees.c.root,
            keycardFile: operatorCodeFirstIndex.keycardFile,
            parserBundleDigest: operatorCodeFirstIndex.fixture.parserBundleDigest,
            parserExecutable: parserBinary,
            parserExecutableSha256: createHash('sha256').update(await readFile(parserBinary)).digest('hex'),
          },
          registration: {
            clientRoot: worktrees.a.root,
            keycardFile: registrationKeycardFile,
          },
        },
        traffic: this.traffic,
      }
      await writeFixtureState(this.statePath, state)
      process.env.OPERATOR_CONSOLE_LIVE_FIXTURE_STATE = this.statePath
    } catch (error) {
      await this.stop()
      throw error
    }
  }

  async stop(): Promise<void> {
    const failures: unknown[] = []
    for (const child of [this.console, this.server]) {
      try {
        await stopChild(child)
      } catch (error) {
        failures.push(error)
      }
    }
    if (this.postgresStarted) {
      try {
        await execute('docker', ['stop', this.containerName], repositoryRoot)
      } catch (error) {
        failures.push(error)
      }
    }
    let worktreesRemoved = true
    if (this.fixtureRoot) {
      try {
        await this.removeWorktrees()
      } catch (error) {
        worktreesRemoved = false
        failures.push(error)
      }
      if (worktreesRemoved) {
        try {
          await rm(this.fixtureRoot, { recursive: true, force: true })
        } catch (error) {
          failures.push(error)
        }
      }
    }
    if (failures.length > 0) {
      throw new AggregateError(failures, 'live fixture cleanup failed')
    }
  }

  private async startPostgres(): Promise<{ dsn: string; port: number }> {
    const password = randomBytes(24).toString('base64url')
    await execute('docker', [
      'run',
      '--detach',
      '--rm',
      '--name', this.containerName,
      '--label', 'engram.fixture=operator-console-live',
      '--label', `engram.fixture.id=${this.fixtureId}`,
      '--env', 'POSTGRES_USER=engram',
      '--env', `POSTGRES_PASSWORD=${password}`,
      '--env', 'POSTGRES_DB=engram',
      '--publish', '127.0.0.1::5432',
      POSTGRES_IMAGE,
    ], repositoryRoot)
    this.postgresStarted = true

    const portResult = await execute('docker', ['port', this.containerName, '5432/tcp'], repositoryRoot)
    const address = portResult.stdout.trim().split(/\r?\n/, 1)[0]
    const port = Number(address.slice(address.lastIndexOf(':') + 1))
    if (!Number.isInteger(port) || port < 1 || port > 65_535) {
      throw new Error(`unable to resolve disposable PostgreSQL port from ${JSON.stringify(address)}`)
    }

    await waitFor(async () => {
      try {
        await execute('docker', ['exec', this.containerName, 'pg_isready', '--username=engram', '--dbname=engram'], repositoryRoot)
        return true
      } catch {
        return false
      }
    }, 'disposable PostgreSQL')

    return {
      dsn: `postgres://engram:${encodeURIComponent(password)}@127.0.0.1:${port}/engram?sslmode=disable`,
      port,
    }
  }

  private async buildServer(commit: string): Promise<string> {
    const binary = join(this.fixtureRoot, process.platform === 'win32' ? 'engram-server.exe' : 'engram-server')
    await execute('go', ['build', '-ldflags', `-X main.SourceCommit=${commit}`, '-o', binary, './cmd/engram-server'], repositoryRoot)
    return binary
  }

  private async buildMCPClient(): Promise<string> {
    const clientDir = join(this.fixtureRoot, 'mcp-install', 'client')
    await mkdir(clientDir, { recursive: true, mode: 0o700 })
    const binary = join(clientDir, process.platform === 'win32' ? 'engram.exe' : 'engram')
    await execute('go', ['build', '-o', binary, './cmd/engram'], repositoryRoot)
    return binary
  }

  private async buildMCPParser(): Promise<string> {
    const parserDir = join(this.fixtureRoot, 'mcp-install', 'client', 'parser')
    await mkdir(parserDir, { recursive: true, mode: 0o700 })
    const binary = join(parserDir, process.platform === 'win32' ? 'parser.exe' : 'parser')
    await execute('go', ['build', '-o', binary, './tools/uci-parser'], repositoryRoot)
    return binary
  }

  private async startServer(binary: string, dsn: string): Promise<{ apiUrl: string; health: { status: number; body: unknown }; ready: RouteTraffic }> {
    const home = join(this.fixtureRoot, 'home')
    const environment = fixtureEnvironment({
      DATABASE_DSN: dsn,
      DATABASE_MAX_CONNS: '4',
      ENGRAM_AUTH_ADMIN_TOKEN: this.adminToken,
      ENGRAM_AUTH_DISABLED: 'false',
      ENGRAM_CODE_INTEL_ENABLED: 'true',
      ENGRAM_VNEXT_F_ENABLED: 'true',
      ENGRAM_WORKER_HOST: '127.0.0.1',
      HOME: home,
      USERPROFILE: home,
      APPDATA: join(home, 'AppData', 'Roaming'),
      LOCALAPPDATA: join(home, 'AppData', 'Local'),
    })
    for (let attempt = 0; attempt < 3; attempt += 1) {
      const port = await reservePort()
      this.server = startProcess(binary, [], repositoryRoot, { ...environment, ENGRAM_WORKER_PORT: String(port) })
      const apiUrl = `http://127.0.0.1:${port}`
      try {
        const ready = await this.awaitReady(`${apiUrl}/api/ready`, this.server, 'Go API')
        const health = await this.fetchJSON(`${apiUrl}/api/health`, { method: 'GET' })
        return { apiUrl, health, ready }
      } catch {
        await stopChild(this.server)
        this.server = undefined
      }
    }
    throw new Error('Go API did not retain its loopback listener after readiness')
  }

  private async provisionOperatorCode(dsn: string, worktree: FixtureWorktreeRuntime, variant: 'a' | 'b' | 'c' | 'd', browserEmail: string, password?: string): Promise<OperatorCodeFixture> {
    const dsnFile = join(this.fixtureRoot, 'operator-code-dsn.txt')
    await writeFile(dsnFile, `${dsn}\n`, { encoding: 'utf8', mode: 0o600 })
    const args = [
      'run', './cmd/operator-code-live-fixture',
      '--dsn-file', dsnFile,
      '--browser-email', browserEmail,
      '--project', `${this.fixtureId}-${variant}`,
      '--source-file', worktree.sourceFile,
    ]
    if (password !== undefined) {
      const passwordFile = join(this.fixtureRoot, `operator-code-browser-${variant}-password.txt`)
      await writeFile(passwordFile, `${password}\n`, { encoding: 'utf8', mode: 0o600 })
      args.push('--password-file', passwordFile)
    }
    const result = await execute('go', args, repositoryRoot)
    const output: unknown = JSON.parse(result.stdout)
    if (output === null || typeof output !== 'object' || Array.isArray(output)) {
      throw new Error('operator-code fixture provisioner returned an invalid non-secret receipt')
    }
    const query = Reflect.get(output, 'query')
    const expectedSearch = Reflect.get(output, 'expectedSearch')
    const expectedGraph = Reflect.get(output, 'expectedGraph')
    const expectedSource = Reflect.get(output, 'expectedSource')
    const expectedMarker = Reflect.get(output, 'expectedMarker')
    if (
      typeof query !== 'string' || query === ''
      || typeof expectedSearch !== 'string' || expectedSearch === ''
      || typeof expectedGraph !== 'string' || expectedGraph === ''
      || typeof expectedSource !== 'string' || expectedSource === ''
      || typeof expectedMarker !== 'string' || expectedMarker === ''
    ) {
      throw new Error('operator-code fixture provisioner returned an invalid non-secret receipt')
    }
    return { query, expectedSearch, expectedGraph, expectedSource, expectedMarker }
  }

  private async provisionNoViewOperatorCode(dsn: string, worktree: FixtureWorktreeRuntime, browserEmail: string): Promise<{ fixture: FirstIndexFixture; keycardFile: string }> {
    const dsnFile = join(this.fixtureRoot, 'operator-code-dsn.txt')
    const keycardFile = join(this.fixtureRoot, 'operator-code-first-index.keycard')
    await writeFile(dsnFile, `${dsn}\n`, { encoding: 'utf8', mode: 0o600 })
    const result = await execute('go', [
      'run', './cmd/operator-code-live-fixture',
      '--dsn-file', dsnFile,
      '--browser-email', browserEmail,
      '--project', `${this.fixtureId}-c`,
      '--source-file', worktree.sourceFile,
      '--mode', 'no-view',
      '--keycard-file', keycardFile,
    ], repositoryRoot)
    const output: unknown = JSON.parse(result.stdout)
    if (output === null || typeof output !== 'object' || Array.isArray(output)) {
      throw new Error('operator-code no-view provisioner returned an invalid non-secret receipt')
    }
    const noView = Reflect.get(output, 'noView')
    if (noView === null || typeof noView !== 'object' || Array.isArray(noView)) {
      throw new Error('operator-code no-view provisioner did not register its checkout')
    }
    const query = Reflect.get(output, 'query')
    const expectedSearch = Reflect.get(output, 'expectedSearch')
    const expectedGraph = Reflect.get(output, 'expectedGraph')
    const expectedSource = Reflect.get(output, 'expectedSource')
    const expectedMarker = Reflect.get(output, 'expectedMarker')
    const sourceId = Reflect.get(noView, 'sourceId')
    const checkoutId = Reflect.get(noView, 'checkoutId')
    const incarnationId = Reflect.get(noView, 'incarnationId')
    const analysisProfileId = Reflect.get(noView, 'analysisProfileId')
    const parserBundleDigest = Reflect.get(noView, 'parserBundleDigest')
    if (
      typeof query !== 'string' || query === '' || typeof expectedSearch !== 'string' || expectedSearch === '' || typeof expectedGraph !== 'string' || expectedGraph === '' || typeof expectedSource !== 'string' || expectedSource === '' || typeof expectedMarker !== 'string' || expectedMarker === ''
      || typeof sourceId !== 'string' || sourceId === '' || typeof checkoutId !== 'string' || checkoutId === '' || typeof incarnationId !== 'string' || incarnationId === '' || typeof analysisProfileId !== 'string' || analysisProfileId === '' || typeof parserBundleDigest !== 'string' || parserBundleDigest === ''
    ) {
      throw new Error('operator-code no-view provisioner returned an invalid non-secret receipt')
    }
    await assertFile(keycardFile, 'no-view fixture keycard')
    return {
      fixture: { query, expectedSearch, expectedGraph, expectedSource, expectedMarker, sourceId, checkoutId, incarnationId, analysisProfileId, parserBundleDigest },
      keycardFile,
    }
  }

  private async createWorktrees(): Promise<FixtureWorktreeSet> {
    const worktreeRoot = join(this.fixtureRoot, 'worktrees')
    const aRoot = join(worktreeRoot, 'a')
    const bRoot = join(worktreeRoot, 'b')
    const cRoot = join(worktreeRoot, 'c')
    await mkdir(aRoot, { recursive: true })
    await this.writeWorktreeSources(aRoot, 'a-before', 'a')
    await execute('git', ['init'], aRoot)
    await execute('git', ['config', 'user.email', 'operator-code-live@example.test'], aRoot)
    await execute('git', ['config', 'user.name', 'Operator Code Live Fixture'], aRoot)
    await execute('git', ['add', '--all'], aRoot)
    await execute('git', ['commit', '--message', 'operator code A fixture'], aRoot)
    await execute('git', ['remote', 'add', 'origin', `https://fixture.invalid/${this.fixtureId}.git`], aRoot)
    await execute('git', ['worktree', 'add', '--detach', bRoot, 'HEAD'], aRoot)
    await execute('git', ['worktree', 'add', '--detach', cRoot, 'HEAD'], aRoot)
    await this.writeWorktreeSources(bRoot, 'b', 'b')
    await execute('git', ['add', '--all'], bRoot)
    await execute('git', ['commit', '--message', 'operator code B fixture'], bRoot)
    await this.writeWorktreeSources(cRoot, 'a-before', 'c')
    await execute('git', ['add', '--all'], cRoot)
    await execute('git', ['commit', '--message', 'operator code C fixture'], cRoot)
    const [linkedB, linkedC] = await Promise.all([lstat(join(bRoot, '.git')), lstat(join(cRoot, '.git'))])
    if (linkedB.isDirectory() || linkedC.isDirectory()) {
      throw new Error('fixture B and C must be Git linked worktrees')
    }
    return {
      a: await this.worktreeRuntime(aRoot),
      b: await this.worktreeRuntime(bRoot),
      c: await this.worktreeRuntime(cRoot),
    }
  }

  private async writeWorktreeSources(root: string, variant: 'a-before' | 'b', marker: 'a' | 'b' | 'c'): Promise<void> {
    const sourceRoot = join(repositoryRoot, 'tests', 'fixtures', 'operator-code-current-slice', 'worktrees', variant, 'src')
    const destination = join(root, 'src')
    await mkdir(destination, { recursive: true })
    for (const name of ['target.ts', 'relay.ts', 'CurrentSlicePanel.tsx']) {
      await writeFile(join(destination, name), await readFile(join(sourceRoot, name)))
    }
    await writeFile(join(root, '.engram-project'), `${JSON.stringify({
      version: 3,
      project_id: this.projectAnchorID,
      name: `${this.fixtureId}-repository`,
      scope: 'repository',
    }, null, 2)}\n`)
    const searchPages = Array.from({ length: 60 }, (_, index) => `
func CodeExplorerFixtureAPage${index + 1}() string {
	return "CodeExplorerFixtureA"
}`).join('')
    await writeFile(join(root, 'fixture.go'), `package fixture

const CodeExplorerFixtureMessage = "operator-code-fixture-${marker}"

func CodeExplorerFixtureB() string { return CodeExplorerFixtureMessage }

func CodeExplorerFixtureA() string {
	const sourceMarker = "operator-code-fixture-${marker}"
	_ = sourceMarker
	return CodeExplorerFixtureB()
}
${searchPages}`)
  }

  private async worktreeRuntime(root: string): Promise<FixtureWorktreeRuntime> {
    const sourceFile = join(root, 'fixture.go')
    const [head, tree, source] = await Promise.all([
      execute('git', ['rev-parse', 'HEAD'], root),
      execute('git', ['rev-parse', 'HEAD^{tree}'], root),
      readFile(sourceFile),
    ])
    return {
      root,
      sourceFile,
      identity: {
        head: head.stdout.trim(),
        tree: tree.stdout.trim(),
        fixtureSourceSha256: createHash('sha256').update(source).digest('hex'),
      },
    }
  }

  private async removeWorktrees(): Promise<void> {
    const aRoot = join(this.fixtureRoot, 'worktrees', 'a')
    const bRoot = join(this.fixtureRoot, 'worktrees', 'b')
    const cRoot = join(this.fixtureRoot, 'worktrees', 'c')
    try {
      await Promise.all([lstat(join(bRoot, '.git')), lstat(join(cRoot, '.git'))])
    } catch {
      return
    }
    await execute('git', ['worktree', 'remove', cRoot], aRoot)
    await execute('git', ['worktree', 'remove', bRoot], aRoot)
    const remaining = await execute('git', ['worktree', 'list', '--porcelain'], aRoot)
    if (remaining.stdout.includes(`worktree ${bRoot}`) || remaining.stdout.includes(`worktree ${cRoot}`)) {
      throw new Error('fixture linked worktrees remained registered after teardown')
    }
  }

  private async startConsole(apiUrl: string): Promise<string> {
    const port = await reservePort()
    this.console = startProcess(process.execPath, ['.output/server/index.mjs'], consoleRoot, {
      ...fixtureEnvironment(),
      PORT: String(port),
      HOST: '127.0.0.1',
      NUXT_OPERATOR_API_TARGET: apiUrl,
      NUXT_PUBLIC_API_DISPLAY_HOST: `127.0.0.1:${port} `,
    })
    return `http://127.0.0.1:${port}`
  }

  private async awaitReady(url: string, child: ChildProcess | undefined, label: string): Promise<RouteTraffic> {
    let lastFailure = ''
    const deadline = Date.now() + READY_TIMEOUT_MS
    while (Date.now() < deadline) {
      if (child?.exitCode !== null || child?.signalCode !== null) {
        throw new Error(`${label} exited before readiness: ${child.exitCode ?? child.signalCode}`)
      }
      try {
        const response = await fetch(url, { signal: AbortSignal.timeout(1_000) })
        const traffic: RouteTraffic = {
          origin: 'bootstrap',
          method: 'GET',
          path: new URL(url).pathname,
          status: response.status,
          at: new Date().toISOString(),
        }
        this.traffic.push(traffic)
        if (response.ok) {
          return traffic
        }
        lastFailure = `${response.status} ${await response.text()}`
      } catch (error) {
        lastFailure = error instanceof Error ? error.message : String(error)
      }
      await delay(200)
    }
    throw new Error(`${label} did not become ready within ${READY_TIMEOUT_MS}ms: ${lastFailure}`)
  }

  private async fetchJSON(url: string, init: RequestInit): Promise<{ status: number; body: unknown }> {
    const response = await fetch(url, { ...init, signal: AbortSignal.timeout(5_000) })
    const text = await response.text()
    let body: unknown
    try {
      body = text === '' ? null : JSON.parse(text)
    } catch {
      body = text
    }
    this.traffic.push({
      origin: 'bootstrap',
      method: init.method ?? 'GET',
      path: new URL(url).pathname,
      status: response.status,
      at: new Date().toISOString(),
    })
    return { status: response.status, body }
  }
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  const fixture = new LiveFixture()
  await fixture.start()
  return async () => fixture.stop()
}

export async function readLiveFixture(): Promise<LiveFixtureState> {
  const statePath = process.env.OPERATOR_CONSOLE_LIVE_FIXTURE_STATE
  if (!statePath) {
    throw new Error('OPERATOR_CONSOLE_LIVE_FIXTURE_STATE was not provided by the live fixture bootstrap')
  }
  const state = JSON.parse(await readFile(statePath, 'utf8')) as LiveFixtureState
  if (state.schema !== STATE_SCHEMA) {
    throw new Error(`unsupported live fixture state schema: ${String(state.schema)}`)
  }
  if (state.mock.prohibited !== true || state.mock.liveConfigContainsMockCommand !== false) {
    throw new Error('live fixture state does not prove mock API exclusion')
  }
  return state
}

export async function appendBrowserTraffic(entries: RouteTraffic[]): Promise<LiveFixtureState> {
  const state = await readLiveFixture()
  state.traffic.push(...entries)
  await writeFixtureState(requiredFixtureStatePath(), state)
  return state
}

async function assertLiveHarnessContract(): Promise<void> {
  const [liveConfig, manifest] = await Promise.all([
    readFile(join(consoleRoot, 'playwright.live.config.ts'), 'utf8'),
    readFile(join(consoleRoot, 'package.json'), 'utf8'),
  ])
  if (liveConfig.includes(FORBIDDEN_MOCK_COMMAND)) {
    throw new Error(`live Playwright configuration must not start ${FORBIDDEN_MOCK_COMMAND}`)
  }
  const manifestData: unknown = JSON.parse(manifest)
  if (
    manifestData === null
    || typeof manifestData !== 'object'
    || Array.isArray(manifestData)
    || !('scripts' in manifestData)
    || manifestData.scripts === null
    || typeof manifestData.scripts !== 'object'
    || Array.isArray(manifestData.scripts)
    || !('test:browser:live' in manifestData.scripts)
    || typeof manifestData.scripts['test:browser:live'] !== 'string'
  ) {
    throw new Error('test:browser:live must build Nuxt and must not start the mock API')
  }
  if (manifestData.scripts['test:browser:live'].includes(FORBIDDEN_MOCK_COMMAND)) {
    throw new Error('test:browser:live must not start the mock API')
  }
}


export function fixtureEnvironment(overrides: NodeJS.ProcessEnv = {}): NodeJS.ProcessEnv {
  const environment = { ...process.env }
  const overrideNames = new Set(Object.keys(overrides).map((name) => name.toUpperCase()))
  for (const name of Object.keys(environment)) {
    if (FIXTURE_OVERRIDDEN_ENVIRONMENT_NAMES[name.toUpperCase()] === true || overrideNames.has(name.toUpperCase())) {
      delete environment[name]
    }
  }
  return { ...environment, ...overrides }
}

async function reservePort(): Promise<number> {
  const { promise, reject, resolve: resolvePort } = Promise.withResolvers<number>()
  const listener = createServer()
  listener.once('error', reject)
  listener.listen(0, '127.0.0.1', () => {
    const address = listener.address()
    listener.close((error) => {
      if (error) {
        reject(error)
        return
      }
      if (!address || typeof address === 'string') {
        reject(new Error('unable to reserve loopback port'))
        return
      }
      resolvePort(address.port)
    })
  })
  return promise
}

function startProcess(command: string, args: string[], cwd: string, env: NodeJS.ProcessEnv): ChildProcess {
  const child = spawn(command, args, {
    cwd,
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true,
  })
  child.stdout?.resume()
  child.stderr?.resume()
  return child
}

async function stopChild(child: ChildProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null || child.signalCode !== null) {
    return
  }
  child.kill('SIGTERM')
  if (await waitForChildExit(child, PROCESS_STOP_TIMEOUT_MS)) {
    return
  }
  child.kill('SIGKILL')
  if (!(await waitForChildExit(child, PROCESS_STOP_TIMEOUT_MS))) {
    throw new Error(`fixture process ${child.pid ?? 'unknown'} did not stop`)
  }
}

async function waitForChildExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  if (child.exitCode !== null || child.signalCode !== null) {
    return true
  }
  const { promise, resolve } = Promise.withResolvers<boolean>()
  const onExit = () => {
    clearTimeout(timer)
    resolve(true)
  }
  const timer = setTimeout(() => {
    child.removeListener('exit', onExit)
    resolve(false)
  }, timeoutMs)
  child.once('exit', onExit)
  return promise
}

async function waitFor(probe: () => Promise<boolean>, label: string): Promise<void> {
  const deadline = Date.now() + READY_TIMEOUT_MS
  while (Date.now() < deadline) {
    if (await probe()) {
      return
    }
    await delay(250)
  }
  throw new Error(`${label} did not become ready within ${READY_TIMEOUT_MS}ms`)
}

async function execute(command: string, args: string[], cwd: string): Promise<CommandResult> {
  const { promise, reject, resolve } = Promise.withResolvers<CommandResult>()
  const child = spawn(command, args, { cwd, windowsHide: true })
  let stdout = ''
  let stderr = ''
  child.stdout?.on('data', (chunk: Buffer) => { stdout += chunk.toString() })
  child.stderr?.on('data', (chunk: Buffer) => { stderr += chunk.toString() })
  child.once('error', reject)
  child.once('close', (code, signal) => {
    if (code === 0) {
      resolve({ stdout, stderr })
      return
    }
    reject(new Error(`${command} ${args.join(' ')} failed (${code ?? signal ?? 'unknown'}): ${stderr || stdout}`))
  })
  return promise
}

async function assertFile(path: string, label: string): Promise<void> {
  try {
    await access(path, constants.F_OK)
  } catch {
    throw new Error(`${label} is missing at ${path}; run npm run build before the live harness`)
  }
}


async function writeFixtureState(path: string, state: LiveFixtureState): Promise<void> {
  const serialized = JSON.stringify(state, null, 2)
  if (serialized.includes('"adminToken"') || serialized.includes('"ENGRAM_AUTH_ADMIN_TOKEN"') || serialized.includes('"clientKeycard"')) {
    throw new Error('live fixture state must not disclose a fixture credential')
  }
  await writeFile(path, serialized, 'utf8')
}

function requiredFixtureStatePath(): string {
  const statePath = process.env.OPERATOR_CONSOLE_LIVE_FIXTURE_STATE
  if (!statePath) {
    throw new Error('OPERATOR_CONSOLE_LIVE_FIXTURE_STATE was not provided by the live fixture bootstrap')
  }
  return statePath
}

function delay(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>()
  setTimeout(resolve, ms)
  return promise
}
