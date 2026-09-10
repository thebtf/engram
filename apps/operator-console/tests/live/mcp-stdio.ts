import { spawn } from 'node:child_process'
import type { ChildProcess } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { mkdtemp, mkdir, readFile, rm } from 'node:fs/promises'
import { createConnection } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
import { fixtureEnvironment } from './fixture-bootstrap'

const REQUEST_TIMEOUT_MS = 60_000
const PROCESS_STOP_TIMEOUT_MS = 10_000
const CONTROL_TIMEOUT_MS = 5_000
const DAEMON_READY_TIMEOUT_MS = 10_000

export interface MCPStdioClientOptions {
  clientRoot: string
  executable: string
  serverURL: string
  token: string
}

export interface MCPStdioTranscript {
  daemonGeneration: string
  daemonPID: number
  externalPID: number
  methods: string[]
  processTreeStopped: boolean
  rootLabel: 'A' | 'B'
  sessionScoped: true
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
  generation: string
  pid: number
}

export class MCPStdioClient {
  private readonly child: ChildProcess
  private readonly controlPath: string
  private readonly methods: string[] = []
  private readonly pending = new Map<string, PendingRequest>()
  private readonly rootLabel: 'A' | 'B'
  private readonly stateRoot: string
  private readonly tools: string[] = []
  private closed = false
  private daemon: MuxDaemonIdentity | undefined
  private nextID = 0
  private processTreeStopped = false
  private stateRootRemoved = false

  private constructor(child: ChildProcess, stateRoot: string, rootLabel: 'A' | 'B', controlPath: string) {
    this.child = child
    this.controlPath = controlPath
    this.stateRoot = stateRoot
    this.rootLabel = rootLabel
    child.on('error', () => this.rejectPending())
    child.on('exit', () => this.rejectPending())
    const stdout = child.stdout
    if (stdout === null) throw new Error('external MCP client did not expose stdout')
    const lines = createInterface({ input: stdout })
    lines.on('line', (line) => this.acceptFrame(line))
  }

  static async start(options: MCPStdioClientOptions): Promise<MCPStdioClient> {
    const rootLabel = clientRootLabel(options.clientRoot)
    const stateRoot = await mkdtemp(join(tmpdir(), `operator-console-live-mcp-${rootLabel.toLowerCase()}-`))
    const dataRoot = join(stateRoot, 'data')
    const home = join(stateRoot, 'home')
    await Promise.all([
      mkdir(dataRoot, { recursive: true, mode: 0o700 }),
      mkdir(home, { recursive: true, mode: 0o700 }),
      mkdir(join(home, 'AppData', 'Roaming'), { recursive: true, mode: 0o700 }),
      mkdir(join(home, 'AppData', 'Local'), { recursive: true, mode: 0o700 }),
    ])
    const child = spawn(options.executable, [], {
      cwd: options.clientRoot,
      env: fixtureEnvironment({
        APPDATA: join(home, 'AppData', 'Roaming'),
        ENGRAM_CLIENT_INSTANCE_ID: '',
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
    child.stderr?.resume()
    if (child.pid === undefined || child.pid <= 0 || child.stdin === null || child.stdout === null) {
      await stopOwnedChild(child)
      await rm(stateRoot, { force: true, recursive: true })
      throw new Error('external MCP client did not start with standard I/O')
    }
    return new MCPStdioClient(child, stateRoot, rootLabel, muxDaemonControlPath(dataRoot))
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
    await this.notify('notifications/initialized', {})
    const listed = await this.request('tools/list', {})
    if (listed === null || typeof listed !== 'object' || Array.isArray(listed) || !('tools' in listed) || !Array.isArray(listed.tools)) {
      throw new Error('external MCP client returned an invalid tools/list response')
    }
    this.tools.length = 0
    for (const tool of listed.tools) {
      if (tool !== null && typeof tool === 'object' && !Array.isArray(tool) && 'name' in tool && typeof tool.name === 'string' && tool.name !== '') this.tools.push(tool.name)
    }
    this.daemon = await readMuxDaemonIdentity(this.controlPath)
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

  transcript(): MCPStdioTranscript {
    const pid = this.child.pid
    if (pid === undefined || pid <= 0) throw new Error('external MCP client lost its process identity')
    return {
      daemonGeneration: this.daemon?.generation ?? '',
      daemonPID: this.daemon?.pid ?? 0,
      externalPID: pid,
      methods: [...this.methods],
      processTreeStopped: this.processTreeStopped,
      rootLabel: this.rootLabel,
      sessionScoped: true,
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
        const daemon = this.daemon ?? await readMuxDaemonIdentity(this.controlPath)
        this.daemon = daemon
        const current = await readMuxDaemonIdentity(this.controlPath)
        if (current.pid !== daemon.pid || current.generation !== daemon.generation) {
          throw new Error('external MCP daemon control metadata changed before shutdown')
        }
        const response = responseRecord(await sendMuxControlRequest(this.controlPath, { cmd: 'shutdown', drain_timeout_ms: 2_000 }))
        if (response.ok !== true) throw new Error('external MCP daemon rejected graceful shutdown')
        await waitForMuxDaemonStop(this.controlPath)
        daemonStopped = true
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
          await rm(this.stateRoot, { force: true, recursive: true })
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

function clientRootLabel(root: string): 'A' | 'B' {
  const normalized = root.replaceAll('\\', '/').replace(/\/+$/, '')
  if (normalized.endsWith('/a')) return 'A'
  if (normalized.endsWith('/b')) return 'B'
  throw new Error('external MCP client root is not a fixture linked worktree')
}

function muxDaemonControlPath(dataRoot: string): string {
  return join(dataRoot, 'engram-muxd.ctl.sock')
}

function muxControlEndpoint(controlPath: string): string {
  if (process.platform !== 'win32') return controlPath
  const digest = createHash('sha256').update(controlPath.toLowerCase()).digest('hex').slice(0, 32)
  return `\\\\.\\pipe\\mcp-mux-${digest}`
}

async function readMuxDaemonIdentity(controlPath: string): Promise<MuxDaemonIdentity> {
  const deadline = Date.now() + DAEMON_READY_TIMEOUT_MS
  let lastError = 'daemon control did not respond'
  while (Date.now() < deadline) {
    try {
      const response = responseRecord(await sendMuxControlRequest(controlPath, { cmd: 'status' }))
      if (response.ok !== true) throw new Error('daemon control rejected status request')
      const status = responseRecord(response.data)
      const identity = muxDaemonIdentity(status)
      const marker = responseRecord(JSON.parse(await readFile(`${controlPath}.marker.json`, 'utf8')))
      if (marker.pid !== identity.pid || marker.daemon_generation !== identity.generation) {
        throw new Error('daemon marker does not match live control metadata')
      }
      return identity
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error)
      await new Promise<void>((resolve) => setTimeout(resolve, 50))
    }
  }
  throw new Error(`external MCP daemon did not publish verified control metadata: ${lastError}`)
}

function muxDaemonIdentity(status: Record<string, unknown>): MuxDaemonIdentity {
  const pid = status.pid
  const generation = status.daemon_generation
  const shuttingDown = status.shutting_down
  if (!Number.isSafeInteger(pid) || pid <= 0 || typeof generation !== 'string' || generation === '' || shuttingDown === true) {
    throw new Error('external MCP daemon control returned invalid live metadata')
  }
  return { generation, pid }
}

async function waitForMuxDaemonStop(controlPath: string): Promise<void> {
  const deadline = Date.now() + PROCESS_STOP_TIMEOUT_MS
  while (Date.now() < deadline) {
    try {
      await sendMuxControlRequest(controlPath, { cmd: 'status' })
    } catch {
      return
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 50))
  }
  throw new Error('external MCP daemon remained reachable after graceful shutdown')
}

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
  socket.once('error', () => fail(new Error('external MCP daemon control request failed')))
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

async function stopOwnedChild(child: ChildProcess): Promise<void> {
  child.stdin?.end()
  if (await waitForExit(child, 2_000)) return
  if (child.pid !== undefined && child.pid > 0) child.kill('SIGTERM')
  await waitForExit(child, PROCESS_STOP_TIMEOUT_MS)
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
