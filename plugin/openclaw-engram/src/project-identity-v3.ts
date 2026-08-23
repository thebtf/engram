import { execFileSync } from 'node:child_process';
import { readFileSync, statSync } from 'node:fs';
import path from 'node:path';

const ANCHOR_FILE = '.engram-project';
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const CONTROL = /[\p{Cc}\p{Cf}]/u;
const LEGACY_SCHEMES: Record<string, true> = {
  anchor_v3: true,
  binding_v2: true,
  git_remote_relative_v2: true,
  git_hash_v2: true,
  path_hash_v1: true,
  legacy_slug: true,
  non_git_anchor_v2: true,
  manual_alias: true,
};

type ProjectAnchorScope = 'repository' | 'directory';
type RemoteDisposition = 'normalized' | 'omitted' | 'refused';

export interface ProjectAnchorV3 {
  version: 3;
  project_id: string;
  name: string;
  scope: ProjectAnchorScope;
}

export interface LegacyIdentifierV3 {
  scheme: string;
  value: string;
  provenance: string;
}

export interface ProjectIdentityV3 {
  version: 3;
  anchor_project_id: string;
  name: string;
  scope: ProjectAnchorScope;
  normalized_git_remotes: string[];
  legacy_identifiers: LegacyIdentifierV3[];
  client_instance_id: string;
}

export type RemoteNormalizationV3 =
  | { disposition: 'normalized'; value: string }
  | { disposition: Exclude<RemoteDisposition, 'normalized'> };

interface DescriptorInputV3 {
  anchor: unknown;
  version?: unknown;
  anchor_project_id?: unknown;
  name?: unknown;
  scope?: unknown;
  normalized_git_remotes?: unknown;
  legacy_identifiers?: unknown;
  client_instance_id?: unknown;
  project_key?: unknown;
}

function anchorInvalid(): never {
  throw new Error('PROJECT_ANCHOR_INVALID');
}

function descriptorInvalid(): never {
  throw new Error('PROJECT_DESCRIPTOR_INVALID');
}


function isSafeText(value: string): boolean {
  return value.length > 0 && value.trim() === value && !/\s/u.test(value) && !CONTROL.test(value) &&
    !value.includes('@') && !/^[^/:\s@]+:[^@\s]+@/u.test(value);
}

export function isValidClientInstanceIdV3(value: unknown): value is string {
  return typeof value === 'string' && Array.from(value).length <= 256 && isSafeText(value) && !/[\\/]/u.test(value);
}

function isValidAnchorName(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && Array.from(value).length <= 256 && !CONTROL.test(value);
}

export function parseProjectAnchorV3(input: unknown): ProjectAnchorV3 {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) anchorInvalid();
  const fields = input as Record<string, unknown>;
  const keys = Object.keys(fields);
  if (keys.length !== 4 || keys.some((key) => !['version', 'project_id', 'name', 'scope'].includes(key))) {
    anchorInvalid();
  }
  if (fields.version !== 3 || typeof fields.project_id !== 'string' || !UUID.test(fields.project_id) ||
    !isValidAnchorName(fields.name) || (fields.scope !== 'repository' && fields.scope !== 'directory')) {
    anchorInvalid();
  }
  return {
    version: 3,
    project_id: fields.project_id,
    name: fields.name,
    scope: fields.scope,
  };
}

function isSelectedGitRoot(root: string): boolean {
  try {
    const gitRoot = execFileSync('git', ['-C', root, 'rev-parse', '--show-toplevel'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
    return path.resolve(root) === path.resolve(gitRoot);
  } catch {
    return false;
  }
}

function isTrackedAnchor(root: string): boolean {
  try {
    execFileSync('git', ['-C', root, 'ls-files', '--error-unmatch', '--', ANCHOR_FILE], { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

/** Reads only the anchor directly under the adapter-selected scope root. */
export function discoverProjectAnchorV3(root: string, selectedScope?: ProjectAnchorScope): ProjectAnchorV3 | null {
  try {
    if (typeof root !== 'string' || !statSync(root).isDirectory()) anchorInvalid();
    const selectedRoot = path.resolve(root);
    const anchor = parseProjectAnchorV3(JSON.parse(readFileSync(path.join(selectedRoot, ANCHOR_FILE), 'utf8')));
    if (selectedScope !== undefined && anchor.scope !== selectedScope) anchorInvalid();
    if (selectedScope === 'repository' && (!isSelectedGitRoot(selectedRoot) || !isTrackedAnchor(selectedRoot))) anchorInvalid();
    return anchor;
  } catch (error) {
    if (error instanceof Error && error.message === 'PROJECT_ANCHOR_INVALID') throw error;
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
    anchorInvalid();
  }
}

function remoteResult(disposition: Exclude<RemoteDisposition, 'normalized'>): RemoteNormalizationV3 {
  return { disposition };
}

function normalizePath(host: string, rawPath: string): RemoteNormalizationV3 {
  const normalizedHost = host.trim().toLowerCase();
  const segments = rawPath.replace(/\\/g, '/').split('/').filter(Boolean);
  if (!normalizedHost || segments.length === 0 || segments.some((segment) => !isSafeText(segment))) return remoteResult('omitted');
  let normalizedPath = segments.join('/');
  if (normalizedPath.endsWith('.git')) normalizedPath = normalizedPath.slice(0, -4);
  const value = `${normalizedHost}/${normalizedPath}`;
  return isValidNormalizedRemote(value) ? { disposition: 'normalized', value } : remoteResult('omitted');
}

function normalizeScpRemote(value: string): RemoteNormalizationV3 | null {
  const colon = value.indexOf(':');
  if (value.includes('://') || colon <= 0 || value.slice(0, colon).includes('/')) return null;
  let host = value.slice(0, colon);
  const rawPath = value.slice(colon + 1);
  if (!rawPath) return remoteResult('omitted');
  if (rawPath.slice(0, rawPath.indexOf('/') === -1 ? rawPath.length : rawPath.indexOf('/')).includes('@')) {
    return remoteResult('refused');
  }
  const at = host.lastIndexOf('@');
  if (at !== -1) {
    const user = host.slice(0, at);
    if (!user || user.includes(':')) return remoteResult('refused');
    host = host.slice(at + 1);
  }
  if (!host || /[@?#[\\\]]/u.test(host)) return remoteResult('omitted');
  return normalizePath(host, rawPath);
}

function normalizeUrlRemote(value: string): RemoteNormalizationV3 {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return remoteResult('omitted');
  }
  const scheme = url.protocol.slice(0, -1).toLowerCase();
  if (!['http', 'https', 'ssh'].includes(scheme) || scheme === 'file') return remoteResult('omitted');
  if ((url.username || url.password) && (scheme !== 'ssh' || url.password)) return remoteResult('refused');
  if (!url.hostname || url.search || url.hash) return remoteResult('omitted');
  const port = url.port;
  const includePort = port && !((scheme === 'http' && port === '80') || (scheme === 'https' && port === '443') || (scheme === 'ssh' && port === '22'));
  return normalizePath(`${url.hostname}${includePort ? `:${port}` : ''}`, url.pathname);
}

/** Converts an observed remote to credential-free canonical evidence. */
export function normalizeGitRemoteV3(input: unknown): RemoteNormalizationV3 {
  if (typeof input === 'object' && input !== null && !Array.isArray(input)) {
    const observation = input as { source?: unknown; form?: unknown };
    if (typeof observation.source !== 'string') {
      return typeof observation.form === 'string' && observation.form.includes('credential') ? remoteResult('refused') : remoteResult('omitted');
    }
    input = observation.source;
  }
  if (typeof input !== 'string') return remoteResult('omitted');
  const value = input.trim();
  if (!value || /^file:/iu.test(value) || /^[\\/]/u.test(value) || /^[A-Za-z]:[\\/]/u.test(value)) return remoteResult('omitted');
  return normalizeScpRemote(value) ?? normalizeUrlRemote(value);
}

function isValidNormalizedRemote(remote: unknown): remote is string {
  if (typeof remote !== 'string' || !isSafeText(remote) || remote.includes('://') || remote.includes('\\')) return false;
  const divider = remote.indexOf('/');
  if (divider <= 0 || divider === remote.length - 1) return false;
  const host = remote.slice(0, divider);
  const remotePath = remote.slice(divider + 1);
  if (remote.includes('?') || remote.includes('#') || remotePath.startsWith('/') || remotePath.endsWith('/') || remotePath.includes('//') || remotePath.endsWith('.git') || host !== host.toLowerCase()) return false;
  const [hostname, port, ...extra] = host.split(':');
  if (!hostname || extra.length > 0 || (port !== undefined && (!/^\d+$/u.test(port) || Number(port) === 0))) return false;
  return hostname.split('.').every((label) => /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/u.test(label));
}

function isValidLegacyIdentifier(value: unknown): value is LegacyIdentifierV3 {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  const identifier = value as Record<string, unknown>;
  return Object.keys(identifier).length === 3 &&
    Object.keys(identifier).every((key) => ['scheme', 'value', 'provenance'].includes(key)) &&
    typeof identifier.scheme === 'string' && Object.hasOwn(LEGACY_SCHEMES, identifier.scheme) &&
    typeof identifier.value === 'string' && isSafeText(identifier.value) &&
    typeof identifier.provenance === 'string' && isSafeText(identifier.provenance);
}

/** Builds a non-persistent V3 descriptor; the caller must supply its opaque installation ID. */
export function buildProjectIdentityV3(input: DescriptorInputV3): ProjectIdentityV3 {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) descriptorInvalid();
  const fields = input as unknown as Record<string, unknown>;
  if (Object.hasOwn(fields, 'project_key')) throw new Error('PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN');
  if (Object.keys(fields).some((key) => !['anchor', 'version', 'anchor_project_id', 'name', 'scope', 'normalized_git_remotes', 'legacy_identifiers', 'client_instance_id'].includes(key))) descriptorInvalid();
  const anchor = parseProjectAnchorV3(fields.anchor);
  if ((fields.version !== undefined && fields.version !== 3) ||
    (fields.anchor_project_id !== undefined && fields.anchor_project_id !== anchor.project_id) ||
    (fields.name !== undefined && fields.name !== anchor.name)) descriptorInvalid();
  if (fields.scope !== undefined && fields.scope !== anchor.scope) throw new Error('PROJECT_SCOPE_MISMATCH');
  const remotes = fields.normalized_git_remotes ?? [];
  const legacy = fields.legacy_identifiers ?? [];
  if (!Array.isArray(remotes) || !remotes.every(isValidNormalizedRemote) || !Array.isArray(legacy) || !legacy.every(isValidLegacyIdentifier) ||
    !isValidClientInstanceIdV3(fields.client_instance_id)) descriptorInvalid();
  return {
    version: 3,
    anchor_project_id: anchor.project_id,
    name: anchor.name,
    scope: anchor.scope,
    normalized_git_remotes: [...remotes],
    legacy_identifiers: legacy.map((identifier) => ({ ...identifier })),
    client_instance_id: fields.client_instance_id,
  };
}
