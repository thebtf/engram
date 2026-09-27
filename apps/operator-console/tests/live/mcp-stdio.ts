import { spawn } from 'node:child_process'
import type { ChildProcess } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { mkdtemp, mkdir, readFile, rm } from 'node:fs/promises'
import { createConnection } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createInterface } from 'node:readline'
import { pathToFileURL } from 'node:url'
import { fixtureEnvironment } from './fixture-bootstrap'

const REQUEST_TIMEOUT_MS = 60_000
const PROCESS_STOP_TIMEOUT_MS = 10_000
const CONTROL_TIMEOUT_MS = 5_000
const DAEMON_READY_TIMEOUT_MS = 10_000

export interface MCPCodeIndexConfig {
  parserBundleDigest: string
  parserExecutable: string
}

export interface MCPNoViewTarget {
  analysisProfileId: string
  checkoutId: string
  incarnationId: string
  sourceId: string
}

export interface MCPStdioClientOptions {
  clientRoot: string
  codeIndex?: MCPCodeIndexConfig
  executable: string
  serverURL: string
  token: string
}

export interface MCPStdioTranscript {
  daemonExecutable: string
  daemonExecutableSha256: string
  daemonGeneration: string
  daemonPID: number
  externalPID: number
  methods: string[]
  processTreeStopped: boolean
  rootLabel: 'A' | 'B' | 'C'
  sessionScoped: true
  stateRemovalAttempts: number
  stateRootRemoved: boolean
  tools: string[]
  usedStdio: true
}

interface PendingRequest {
  method: string
  reject: (reason?: unknown) => void
  resolve: (value: unknown) => void
}

interface MuxDaemonIdentity {
  executable: string
  executableSha256: string
  generation: string
  pid: number
}

interface MuxDaemonStatus {
  generation: string
  pid: number
  shuttingDown: boolean
}

export class MCPStdioClient {
  private readonly child: ChildProcess
  private readonly daemonIdentity: Promise<MuxDaemonIdentity>
  private readonly controlPath: string
  private readonly methods: string[] = []
  private readonly pending = new Map<string, PendingRequest>()
  private readonly rootLabel: 'A' | 'B' | 'C'
  private readonly stateRoot: string
  private readonly tools: string[] = []
  private stderrSample = ''
  private closed = false
  private daemon: MuxDaemonIdentity | undefined
  private nextID = 0
  private processTreeStopped = false
  private stateRemovalAttempts = 0
  private stateRootRemoved = false
  private constructor(child: ChildProcess, stateRoot: string, rootLabel: 'A' | 'B' | 'C', controlPath: string, expectedExecutable: string) {
    this.child = child
    this.controlPath = controlPath
    this.daemonIdentity = readMuxDaemonIdentity(controlPath, expectedExecutable, () => this.failureDiagnostics())
    this.stateRoot = stateRoot
    this.rootLabel = rootLabel
    child.on('error', () => this.rejectPending())
    child.stderr?.on('data', (chunk: Buffer) => {
      if (this.stderrSample.length < 8192) this.stderrSample += chunk.toString('utf8').slice(0, 8192 - this.stderrSample.length)
    })
    child.on('exit', () => this.rejectPending())
    const stdout = child.stdout
    if (stdout === null) throw new Error('external MCP client did not expose stdout')
    const lines = createInterface({ input: stdout })
    lines.on('line', (line) => this.acceptFrame(line))
  }

  static async start(options: MCPStdioClientOptions): Promise<MCPStdioClient> {
    const rootLabel = clientRootLabel(options.clientRoot)
    const stateRoot = await mkdtemp(join(tmpdir(), `operator-console-live-mcp-${rootLabel.toLowerCase()}-`))
    const clientInstanceID = randomUUID()
    const dataRoot = join(stateRoot, 'data')
    const home = join(stateRoot, 'home')
    await Promise.all([
      mkdir(dataRoot, { recursive: true, mode: 0o700 }),
      mkdir(home, { recursive: true, mode: 0o700 }),
      mkdir(join(home, 'AppData', 'Roaming'), { recursive: true, mode: 0o700 }),
      mkdir(join(home, 'AppData', 'Local'), { recursive: true, mode: 0o700 }),
    ])
    if (options.codeIndex !== undefined && (options.codeIndex.parserBundleDigest === '' || options.codeIndex.parserExecutable === '')) {
      await rm(stateRoot, { force: true, recursive: true })
      throw new Error('external MCP client code index configuration is incomplete')
    }
    const child = spawn(options.executable, [], {
      cwd: options.clientRoot,
      env: fixtureEnvironment({
        APPDATA: join(home, 'AppData', 'Roaming'),
        ENGRAM_CLIENT_INSTANCE_ID: clientInstanceID,
        ...(options.codeIndex === undefined ? {} : {
          ENGRAM_CODE_INTEL_ENABLED: 'true',
          ENGRAM_UCI_PARSER_BUNDLE_DIGEST: options.codeIndex.parserBundleDigest,
          ENGRAM_UCI_PARSER_EXECUTABLE: options.codeIndex.parserExecutable,
        }),
        ENGRAM_DATA_DIR: dataRoot,
        ENGRAM_TOKEN: options.token,
        ENGRAM_URL: options.serverURL,
        HOME: home,
        LOCALAPPDATA: join(home, 'AppData', 'Local'),
        TEMP: dataRoot,
        TMP: dataRoot,
        TMPDIR: dataRoot,
        USERPROFILE: home,
      }),
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,
    })
    if (child.pid === undefined || child.pid <= 0 || child.stdin === null || child.stdout === null) {
      child.stdin?.end()
      await waitForExit(child, 2_000)
      throw new Error('external MCP client did not start with standard I/O')
    }
    return new MCPStdioClient(child, stateRoot, rootLabel, muxDaemonControlPath(dataRoot, clientInstanceID), options.executable)
  }

  async initializeAndList(): Promise<void> {
    const initialized = await this.request('initialize', {
      capabilities: {},
      clientInfo: { name: 'operator-console-live-topology', version: '1' },
      protocolVersion: '2025-11-25',
    })
    if (
      initialized === null || typeof initialized !== 'object' || Array.isArray(initialized)
      || !('protocolVersion' in initialized) || typeof initialized.protocolVersion !== 'string' || initialized.protocolVersion === ''
    ) {
      throw new Error('external MCP client returned an invalid initialize response')
    }
    this.daemon = await this.daemonIdentity
    await this.notify('notifications/initialized', {})
    const listed = await this.request('tools/list', {})
    if (listed === null || typeof listed !== 'object' || Array.isArray(listed) || !('tools' in listed) || !Array.isArray(listed.tools)) {
      throw new Error('external MCP client returned an invalid tools/list response')
    }
    this.tools.length = 0
    for (const tool of listed.tools) {
      if (tool !== null && typeof tool === 'object' && !Array.isArray(tool) && 'name' in tool && typeof tool.name === 'string' && tool.name !== '') this.tools.push(tool.name)
    }
  }
  async registerProjectIdentity(): Promise<void> {
    if (!this.tools.includes('project_identity.register_v3')) throw new Error('external MCP client did not expose V3 project registration')
    const result = record(await this.request('tools/call', { arguments: {}, name: 'project_identity.register_v3' }))
    const content = Reflect.get(result, 'content')
    if (Reflect.get(result, 'isError') !== false || !Array.isArray(content) || content.length !== 1 || Reflect.get(record(content[0]), 'outcome') !== 'PROJECT_RESOLVED') {
      throw new Error('external MCP client did not complete V3 project registration')
    }
  }

  async readOnlySearch(query: string): Promise<void> {
    const result = await this.request('tools/call', {
      arguments: { action: 'search', limit: 1, query },
      name: 'recall',
    })
    if (result === null || typeof result !== 'object' || Array.isArray(result) || ('isError' in result && result.isError === true)) {
      throw new Error('external MCP client returned an invalid read-only search response')
    }
  }

  async prepareNoViewIndexTarget(target: MCPNoViewTarget): Promise<void> {
    const selected = record(await this.callTool('codebase_context', {
      action: 'select',
      checkout: {
        analysis_profile_id: target.analysisProfileId,
        checkout_id: target.checkoutId,
        incarnation_id: target.incarnationId,
        source_id: target.sourceId,
      },
    }, 'proxy'))
    const contextHandle = Reflect.get(selected, 'context_handle')
    if (
      Reflect.get(selected, 'binding_kind') !== 'checkout'
      || typeof contextHandle !== 'string' || contextHandle === ''
      || Reflect.get(selected, 'source_id') !== target.sourceId
      || Reflect.get(selected, 'checkout_id') !== target.checkoutId
      || Reflect.get(selected, 'incarnation_id') !== target.incarnationId
      || Reflect.get(selected, 'analysis_profile_id') !== target.analysisProfileId
      || Reflect.get(selected, 'context') !== null
    ) {
      throw new Error('external MCP client did not retain the no-view checkout binding')
    }
    const status = record(await this.callTool('codebase_status', { context_handle: contextHandle }, 'direct'))
    if (Reflect.get(status, 'current_context') !== null || Reflect.get(status, 'server_counts_available') !== false) {
      throw new Error('external MCP client did not prepare a no-view index target')
    }
  }

  async preparePublishedIndexTarget(target: MCPNoViewTarget): Promise<void> {
    const selected = record(await this.callTool('codebase_context', {
      action: 'select',
      checkout: {
        analysis_profile_id: target.analysisProfileId,
        checkout_id: target.checkoutId,
        incarnation_id: target.incarnationId,
        source_id: target.sourceId,
      },
    }, 'proxy'))
    const contextHandle = Reflect.get(selected, 'context_handle')
    if (typeof contextHandle !== 'string' || contextHandle === '' || Reflect.get(selected, 'source_id') !== target.sourceId || Reflect.get(selected, 'checkout_id') !== target.checkoutId || Reflect.get(selected, 'binding_kind') !== 'checkout' || Reflect.get(selected, 'context') === null) {
      throw new Error('external MCP client did not resolve the published checkout after restart')
    }
    const status = record(await this.callTool('codebase_status', { context_handle: contextHandle }, 'direct'))
    if (Reflect.get(status, 'current_context') === null || Reflect.get(status, 'server_counts_available') !== true) {
      throw new Error('external MCP client did not prepare the published checkout after restart')
    }
  }
  async registerDirtyCheckout(source: { label?: string; id?: string; root: string }): Promise<Record<string, unknown>> {
    const result = record(await this.callTool('codebase_context', {
      action: 'register', locator: pathToFileURL(source.root).href,
      ...(source.id === undefined ? { source_label: source.label } : { source_id: source.id }),
    }, 'proxy'))
    if (Reflect.get(result, 'binding_kind') !== 'checkout' || Reflect.get(result, 'context') !== null || typeof Reflect.get(result, 'context_handle') !== 'string') throw new Error('ordinary registration did not return an unindexed checkout')
    return result
  }

  async indexDirtyCheckout(contextHandle: string): Promise<string> {
    const result = record(await this.callTool('codebase_index', { context_handle: contextHandle }, 'direct'))
    const runID = Reflect.get(result, 'run_id')
    if (Reflect.get(result, 'status') !== 'started' || typeof runID !== 'string' || !runID) throw new Error('ordinary dirty index did not start')
    return runID
  }

  async dirtyIndexStatus(contextHandle: string, barrier?: string): Promise<Record<string, unknown>> {
    return record(await this.callTool('codebase_status', { context_handle: contextHandle, ...(barrier === undefined ? {} : { after_barrier: { token: barrier, wait_ms: 60_000 } }) }, 'direct'))
  }

  async selectDirtyCheckout(target: MCPNoViewTarget): Promise<Record<string, unknown>> {
    return record(await this.callTool('codebase_context', { action: 'select', checkout: { source_id: target.sourceId, checkout_id: target.checkoutId, incarnation_id: target.incarnationId, analysis_profile_id: target.analysisProfileId } }, 'proxy'))
  }

  async dirtySearch(query: string): Promise<Record<string, unknown>> {
    return record(await this.callTool('codebase_search', { query, limit: 50 }, 'direct'))
  }


  transcript(): MCPStdioTranscript {
    const pid = this.child.pid
    if (pid === undefined || pid <= 0) throw new Error('external MCP client lost its process identity')
    return {
      daemonExecutable: this.daemon?.executable ?? '',
      daemonExecutableSha256: this.daemon?.executableSha256 ?? '',
      daemonGeneration: this.daemon?.generation ?? '',
      daemonPID: this.daemon?.pid ?? 0,
      externalPID: pid,
      methods: [...this.methods],
      processTreeStopped: this.processTreeStopped,
      rootLabel: this.rootLabel,
      sessionScoped: true,
      stateRemovalAttempts: this.stateRemovalAttempts,
      stateRootRemoved: this.stateRootRemoved,
      tools: [...this.tools],
      usedStdio: true,
    }
  }

  async close(): Promise<void> {
    if (this.closed) return
    const failures: unknown[] = []
    let daemonStopped = false
    let childStopped = false
    try {
      try {
        this.daemon ??= await this.daemonIdentity
        daemonStopped = await stopRetainedMuxDaemon(this.controlPath, this.daemon)
      } catch (error) {
        failures.push(error)
      }
      try {
        this.child.stdin?.end()
        if (!(await waitForExit(this.child, PROCESS_STOP_TIMEOUT_MS))) {
          throw new Error('external MCP shim did not exit after supported daemon shutdown')
        }
        childStopped = true
      } catch (error) {
        failures.push(error)
      }
      if (daemonStopped && childStopped) {
        try {
          this.stateRemovalAttempts = await removeStateRoot(this.stateRoot)
          this.stateRootRemoved = true
        } catch (error) {
          failures.push(error)
        }
      }
    } finally {
      this.closed = true
      this.rejectPending()
      this.processTreeStopped = daemonStopped && childStopped && this.stateRootRemoved
    }
    if (failures.length > 0) throw new AggregateError(failures, 'external MCP client cleanup failed')
  }

  private failureDiagnostics(): string {
    const stderr = this.stderrSample.toLowerCase()
    const classes = [
      'muxcore daemon version reconciliation failed', 'muxcore shim setup failed',
      'muxcore shim terminated', 'lifecycle start failed', 'legacy relay start failed',
      'muxcore engine terminated before product control publication',
      'permission denied', 'access is denied', 'address already in use',
      'no such file or directory', 'panic:',
    ].filter((phrase) => stderr.includes(phrase))
    return `root=${this.rootLabel}; shimExit=${this.child.exitCode ?? 'running'}; signal=${this.child.signalCode ?? 'none'}; stderrClass=${classes.join('|') || 'unclassified'}; stderrSha256=${createHash('sha256').update(this.stderrSample).digest('hex')}`
  }

  private acceptFrame(line: string): void {
    let frame: unknown
    try {
      frame = JSON.parse(line)
    } catch {
      return
    }
    if (frame === null || typeof frame !== 'object' || Array.isArray(frame) || !('id' in frame) || typeof frame.id !== 'string') return
    const pending = this.pending.get(frame.id)
    if (pending === undefined) return
    this.pending.delete(frame.id)
    if ('error' in frame) {
      pending.reject(safeRPCError(pending.method, frame.error))
      return
    }
    if (!('result' in frame)) {
      pending.reject(new Error('external MCP client returned an invalid RPC response'))
      return
    }
    pending.resolve(frame.result)
  }

  private async notify(method: string, params: Record<string, never>): Promise<void> {
    this.methods.push(method)
    await this.writeFrame({ jsonrpc: '2.0', method, params })
  }

  private async callTool(name: string, arguments_: Record<string, unknown>, shape: 'direct' | 'proxy'): Promise<unknown> {
    const envelope = record(await this.request('tools/call', { arguments: arguments_, name }))
    const envelopeKeys = Object.keys(envelope).sort().join(',')
    const content = Reflect.get(envelope, 'content')
    if (envelopeKeys !== 'content,isError' || Reflect.get(envelope, 'isError') !== false || !Array.isArray(content) || content.length !== 1) {
      throw new Error(`external MCP client ${name} returned an invalid tool envelope`)
    }
    if (shape === 'direct') return content[0]
    const block = record(content[0])
    if (Object.keys(block).sort().join(',') !== 'text,type' || Reflect.get(block, 'type') !== 'text' || typeof Reflect.get(block, 'text') !== 'string') {
      throw new Error(`external MCP client ${name} returned an invalid tool content block`)
    }
    let proxyEnvelope: Record<string, unknown>
    try {
      proxyEnvelope = record(JSON.parse(Reflect.get(block, 'text') as string))
    } catch {
      throw new Error(`external MCP client ${name} returned a non-JSON proxy envelope`)
    }
    const proxyKeys = Object.keys(proxyEnvelope).sort().join(',')
    const proxyContent = Reflect.get(proxyEnvelope, 'content')
    const proxyIsError = Reflect.get(proxyEnvelope, 'isError')
    if ((proxyKeys !== 'content' && proxyKeys !== 'content,isError') || (proxyIsError !== undefined && typeof proxyIsError !== 'boolean') || !Array.isArray(proxyContent) || proxyContent.length !== 1) {
      throw new Error(`external MCP client ${name} returned an invalid proxy tool envelope`)
    }
    if (proxyIsError === true) throw new Error(`external MCP client ${name} returned a proxy tool error`)
    const proxyBlock = record(proxyContent[0])
    if (Object.keys(proxyBlock).sort().join(',') !== 'text,type' || Reflect.get(proxyBlock, 'type') !== 'text' || typeof Reflect.get(proxyBlock, 'text') !== 'string') {
      throw new Error(`external MCP client ${name} returned an invalid proxy tool content block`)
    }
    try {
      return JSON.parse(Reflect.get(proxyBlock, 'text') as string)
    } catch {
      throw new Error(`external MCP client ${name} returned non-JSON proxy tool content`)
    }
  }

  private async request(method: string, params: Record<string, unknown>): Promise<unknown> {
    if (this.closed) throw new Error('external MCP client is closed')
    const id = `${this.rootLabel.toLowerCase()}-${++this.nextID}-${randomUUID()}`
    const { promise, reject, resolve } = Promise.withResolvers<unknown>()
    this.pending.set(id, { method, reject, resolve })
    this.methods.push(method)
    const timeout = setTimeout(() => {
      const pending = this.pending.get(id)
      if (pending === undefined) return
      this.pending.delete(id)
      pending.reject(new Error(`external MCP client timed out during ${method}`))
    }, REQUEST_TIMEOUT_MS)
    try {
      await this.writeFrame({ id, jsonrpc: '2.0', method, params })
      return await promise
    } finally {
      clearTimeout(timeout)
      this.pending.delete(id)
    }
  }

  private async writeFrame(frame: Record<string, unknown>): Promise<void> {
    const stdin = this.child.stdin
    if (stdin === null || !stdin.writable) throw new Error('external MCP client stdin is unavailable')
    await new Promise<void>((resolve, reject) => {
      stdin.write(`${JSON.stringify(frame)}\n`, 'utf8', (error) => {
        if (error === undefined || error === null) {
          resolve()
          return
        }
        reject(new Error('external MCP client stdin write failed'))
      })
    })
  }

  private rejectPending(): void {
    for (const pending of this.pending.values()) pending.reject(new Error('external MCP client exited before responding'))
    this.pending.clear()
  }
}

function safeRPCError(method: string, value: unknown): Error {
  let code = 'unknown'
  let category = 'unknown'
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    if ('code' in value && typeof value.code === 'number' && Number.isSafeInteger(value.code)) code = String(value.code)
    if ('message' in value && typeof value.message === 'string') {
      if (value.message.includes('project identity v2: PROJECT_IDENTITY_INVALID')) category = 'project_identity_v2_invalid'
      else if (value.message.includes('project identity v2: resolve git identity') && value.message.includes('not a git repository')) category = 'project_identity_v2_not_git'
      else if (value.message.includes('project identity v2: resolve git identity') && value.message.includes('executable file not found')) category = 'project_identity_v2_git_missing'
      else if (value.message.includes('project identity v2: resolve git identity')) category = 'project_identity_v2_git'
      else if (value.message.includes('project identity v2: publish .engram-project-v2.json')) category = 'project_identity_v2_anchor'
      else if (value.message.includes('project identity v2:')) category = 'project_identity_v2'
      else if (value.message.includes('project identity metadata is invalid')) category = 'project_identity_invalid'
      else if (value.message.includes('project identity selector is ambiguous')) category = 'project_identity_ambiguous'
      else if (value.message.includes('project identity registration is unavailable')) category = 'project_identity_unavailable'
      else if (value.message.includes('project identity')) category = 'project_identity'
      else if (value.message.includes('Unauthenticated')) category = 'unauthenticated'
      else if (value.message.includes('PermissionDenied')) category = 'permission_denied'
      else if (value.message.includes('Unavailable')) category = 'transport_unavailable'
    }
  }
  return new Error(`external MCP client ${method} returned an RPC error [${code}; ${category}]`)
}

function clientRootLabel(root: string): 'A' | 'B' | 'C' {
  const normalized = root.replaceAll('\\', '/').replace(/\/+$/, '')
  if (normalized.endsWith('/a') || normalized.endsWith('/dirty-a')) return 'A'
  if (normalized.endsWith('/b') || normalized.endsWith('/dirty-b')) return 'B'
  if (normalized.endsWith('/c')) return 'C'
  throw new Error('external MCP client root is not a fixture linked worktree')
}

function muxDaemonControlPath(dataRoot: string, clientInstanceID: string): string {
  const namespace = `engram-${createHash('sha256').update(clientInstanceID).digest('hex').slice(0, 32)}`
  return join(dataRoot, `${namespace}-muxd.ctl.sock`)
}

function muxControlEndpoint(controlPath: string): string {
  if (process.platform !== 'win32') return controlPath
  const digest = createHash('sha256').update(controlPath.toLowerCase()).digest('hex').slice(0, 32)
  return `\\\\.\\pipe\\mcp-mux-${digest}`
}

async function readMuxDaemonIdentity(controlPath: string, expectedExecutable: string, diagnostics: () => string): Promise<MuxDaemonIdentity> {
  const deadline = Date.now() + DAEMON_READY_TIMEOUT_MS
  let lastError = 'daemon control did not respond'
  while (Date.now() < deadline) {
    try {
      const status = await readMuxDaemonStatus(controlPath)
      if (status.shuttingDown) throw new Error('external MCP daemon control returned invalid live metadata')
      const marker = responseRecord(JSON.parse(await readFile(`${controlPath}.marker.json`, 'utf8')))
      const executable = Reflect.get(marker, 'exe')
      const executablePath = typeof executable === 'string' && executable !== '' ? resolve(executable) : ''
      if (
        marker.pid !== status.pid
        || marker.daemon_generation !== status.generation
        || executablePath === ''
        || executablePath.toLowerCase() !== resolve(expectedExecutable).toLowerCase()
      ) {
        throw new Error('daemon marker does not match the launched client executable and live control metadata')
      }
      return {
        executable: executablePath,
        executableSha256: createHash('sha256').update(await readFile(executablePath)).digest('hex'),
        generation: status.generation,
        pid: status.pid,
      }
    } catch (error) {
      lastError = error instanceof MuxControlUnavailableError ? 'control-unavailable' : error !== null && typeof error === 'object' && Reflect.get(error, 'code') === 'ENOENT' ? 'marker-missing' : error instanceof Error && error.message.startsWith('daemon marker does not match') ? 'identity-mismatch' : error instanceof Error && error.message.startsWith('external MCP daemon control') ? 'control-invalid' : 'unknown'
      await new Promise<void>((resolveDelay) => setTimeout(resolveDelay, 50))
    }
  }
  throw new Error(`external MCP daemon did not publish verified control metadata: ${lastError}; ${diagnostics()}`)
}

async function readMuxDaemonStatus(controlPath: string): Promise<MuxDaemonStatus> {
  const response = responseRecord(await sendMuxControlRequest(controlPath, { cmd: 'status' }))
  if (response.ok !== true) throw new Error('daemon control rejected status request')
  return muxDaemonStatus(responseRecord(response.data))
}

function muxDaemonStatus(status: Record<string, unknown>): MuxDaemonStatus {
  const pid = status.pid
  const generation = status.daemon_generation
  const shuttingDown = status.shutting_down
  if (typeof pid !== 'number' || !Number.isSafeInteger(pid) || pid <= 0 || typeof generation !== 'string' || generation === '' || typeof shuttingDown !== 'boolean') {
    throw new Error('external MCP daemon control returned invalid metadata')
  }
  return { generation, pid, shuttingDown }
}

async function waitForMuxDaemonStop(controlPath: string, daemon: MuxDaemonIdentity): Promise<void> {
  const deadline = Date.now() + PROCESS_STOP_TIMEOUT_MS
  while (Date.now() < deadline) {
    try {
      const current = await readMuxDaemonStatus(controlPath)
      if (current.pid !== daemon.pid || current.generation !== daemon.generation) {
        throw new Error('external MCP daemon control metadata changed during shutdown')
      }
    } catch (error) {
      if (error instanceof MuxControlUnavailableError) return
      throw error
    }
    await new Promise<void>((resolveDelay) => setTimeout(resolveDelay, 50))
  }
  throw new Error('external MCP daemon remained reachable after graceful shutdown')
}

async function stopRetainedMuxDaemon(controlPath: string, daemon: MuxDaemonIdentity): Promise<boolean> {
  const current = await readMuxDaemonStatus(controlPath)
  if (current.pid !== daemon.pid || current.generation !== daemon.generation) {
    throw new Error('external MCP daemon control metadata changed before shutdown')
  }
  if (!current.shuttingDown) {
    const response = responseRecord(await sendMuxControlRequest(controlPath, { cmd: 'shutdown', drain_timeout_ms: 2_000 }))
    if (response.ok !== true) throw new Error('external MCP daemon rejected graceful shutdown')
  }
  await waitForMuxDaemonStop(controlPath, daemon)
  await waitForProcessExit(daemon.pid)
  return true
}

async function waitForProcessExit(pid: number): Promise<void> {
  const deadline = Date.now() + PROCESS_STOP_TIMEOUT_MS
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0)
    } catch (error) {
      if (error !== null && typeof error === 'object' && Reflect.get(error, 'code') === 'ESRCH') return
      throw error
    }
    await new Promise<void>((resolveDelay) => setTimeout(resolveDelay, 50))
  }
  throw new Error('external MCP daemon process remained alive after graceful shutdown')
}

async function removeStateRoot(stateRoot: string): Promise<number> {
  const deadline = Date.now() + PROCESS_STOP_TIMEOUT_MS
  let attempts = 0
  for (; ;) {
    attempts += 1
    try {
      await rm(stateRoot, { force: true, recursive: true })
      return attempts
    } catch (error) {
      if (process.platform !== 'win32' || error === null || typeof error !== 'object' || Reflect.get(error, 'code') !== 'EBUSY' || Date.now() >= deadline) throw error
      await new Promise<void>((resolveDelay) => setTimeout(resolveDelay, 50))
    }
  }
}

class MuxControlUnavailableError extends Error { }

async function sendMuxControlRequest(controlPath: string, request: Record<string, unknown>): Promise<unknown> {
  const socket = createConnection({ path: muxControlEndpoint(controlPath) })
  const { promise, reject, resolve } = Promise.withResolvers<unknown>()
  let complete = false
  let payload = ''
  const fail = (error: Error): void => {
    if (complete) return
    complete = true
    socket.destroy()
    reject(error)
  }
  socket.setTimeout(CONTROL_TIMEOUT_MS)
  socket.once('connect', () => { socket.write(`${JSON.stringify(request)}\n`, 'utf8') })
  socket.on('data', (chunk: Buffer) => {
    payload += chunk.toString('utf8')
    const newline = payload.indexOf('\n')
    if (newline < 0 || complete) return
    complete = true
    socket.end()
    try {
      resolve(JSON.parse(payload.slice(0, newline)))
    } catch {
      reject(new Error('external MCP daemon returned invalid control JSON'))
    }
  })
  socket.once('timeout', () => fail(new Error('external MCP daemon control request timed out')))
  socket.once('error', () => fail(new MuxControlUnavailableError('external MCP daemon control request failed')))
  socket.once('end', () => {
    if (!complete) fail(new Error('external MCP daemon control closed without a response'))
  })
  return promise
}

function responseRecord(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('external MCP daemon control returned a non-object response')
  }
  return value
}

function record(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('external MCP client returned a non-object tool payload')
  }
  return value
}


async function waitForExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  if (child.exitCode !== null || child.signalCode !== null) return true
  const { promise, resolve } = Promise.withResolvers<boolean>()
  const timer = setTimeout(() => {
    child.removeListener('exit', exited)
    resolve(false)
  }, timeoutMs)
  const exited = () => {
    clearTimeout(timer)
    resolve(true)
  }
  child.once('exit', exited)
  return promise
}
