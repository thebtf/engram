/**
 * Project identity resolution.
 *
 * Priority order:
 *   1. agentId (always available from OpenClaw, stable per session)
 *   2. Git remote origin URL + relative path (cross-platform, cross-OS-path stable)
 *   3. Absolute workspace path hash (fallback for non-git directories)
 *
 * The agentId is the PRIMARY scope for OpenClaw — every agent session has a unique
 * stable identifier. Git-based ID is computed when workspaceDir is available so that
 * observations can be shared across agents working in the same repository.
 */

import { createHash, randomBytes } from 'node:crypto';
import { execSync } from 'node:child_process';
import { closeSync, fsyncSync, linkSync, openSync, readFileSync, unlinkSync, writeFileSync } from 'node:fs';
import { resolve, basename } from 'node:path';

// Module-level memoization cache — keyed by resolved cwd path
const gitRemoteCache = new Map<string, GitRemoteResult | null>();

export interface ProjectIdentity {
  /** Primary project identifier used for engram scoping. */
  projectId: string;
  /** The agentId from OpenClaw (always set). */
  agentId: string;
  /** Git remote URL if the workspace is a git repo with a remote. */
  gitRemote?: string;
  /** Relative path within the git repo, if applicable. */
  relativePath?: string;
  /** Full metadata for v2-aware transports. Never derived from agentId. */
  projectIdentityV2?: ProjectIdentityV2;
}

export const PROJECT_IDENTITY_VERSION_V2 = 2 as const;

export interface ProjectIdentityV2 {
  version: 2;
  legacy_project_id: string;
  display_name: string;
  git_remote: string;
  relative_path: string;
  non_git_anchor: string;
  anchor_shared: boolean | null;
}

interface ProjectIdentityV2Input {
  legacy_project_id?: string;
  display_name?: string;
  git_remote?: string;
  relative_path?: string;
  non_git_anchor?: string;
  anchor_shared?: boolean | null;
}

const projectIdentityV2File = '.engram-project-v2.json';
const strictAnchorV2 = /^[0-9a-f]{32}$/;
const projectIdentityControl = /\p{Cc}/u;
const projectSelectorV2 = /^[A-Za-z0-9_.\/:\\-]+$/;
const projectAnchorV2Keys = ['anchor', 'shared', 'version'];

export function validateProjectSelectorV2(selector: unknown): string {
  if (typeof selector !== 'string' || selector === '' || selector.length > 256 ||
      selector.trim() !== selector || selector.includes('..') ||
      projectIdentityControl.test(selector) || !projectSelectorV2.test(selector)) {
    throw new Error('PROJECT_IDENTITY_INVALID: project selector is empty or malformed');
  }
  return selector;
}

export function buildProjectIdentityV2(input: ProjectIdentityV2Input): ProjectIdentityV2 {
  return {
    version: PROJECT_IDENTITY_VERSION_V2,
    legacy_project_id: input.legacy_project_id ?? '',
    display_name: input.display_name ?? '',
    git_remote: input.git_remote ?? '',
    relative_path: input.relative_path ?? '',
    non_git_anchor: input.non_git_anchor ?? '',
    anchor_shared: input.anchor_shared ?? null,
  };
}

export function validateProjectIdentityV2(identity: ProjectIdentityV2): ProjectIdentityV2 {
  const invalid = (reason: string): never => {
    throw new Error(`PROJECT_IDENTITY_INVALID: ${reason}`);
  };
  if (!identity || typeof identity !== 'object' || identity.version !== PROJECT_IDENTITY_VERSION_V2) invalid('unsupported version');
  for (const field of ['legacy_project_id', 'display_name', 'git_remote', 'relative_path', 'non_git_anchor'] as const) {
    if (typeof identity[field] !== 'string') invalid(`${field} must be a string`);
  }
  if (identity.anchor_shared !== null && typeof identity.anchor_shared !== 'boolean') invalid('anchor_shared must be a JSON boolean or null');
  if (identity.legacy_project_id.length > 256 || identity.display_name.length > 256 ||
      identity.legacy_project_id.trim() !== identity.legacy_project_id ||
      identity.display_name.trim() !== identity.display_name ||
      projectIdentityControl.test(identity.legacy_project_id) || projectIdentityControl.test(identity.display_name)) {
    invalid('selector or display name is malformed');
  }
  const hasGit = identity.git_remote !== '' || identity.relative_path !== '';
  const hasAnchor = identity.non_git_anchor !== '' || identity.anchor_shared !== null;
  if (hasGit === hasAnchor) invalid('exactly one identity source is required');
  if (hasGit) {
    if (!identity.git_remote || identity.git_remote.length > 2048 || identity.git_remote.trim() !== identity.git_remote || projectIdentityControl.test(identity.git_remote)) {
      invalid('git_remote is missing or malformed');
    }
    if (!normalizedProjectRelativePathV2(identity.relative_path)) {
      invalid('relative_path is not normalized');
    }
  } else if (!strictAnchorV2.test(identity.non_git_anchor) || typeof identity.anchor_shared !== 'boolean') {
    invalid('non-git anchor must be 128-bit lowercase hex with explicit sharing');
  }
  return identity;
}

function normalizedProjectRelativePathV2(value: string): boolean {
  if (value === '') return true;
  if (value.length > 4096 || value.trim() !== value || value.startsWith('/') ||
      !value.endsWith('/') || value.includes('\\') || projectIdentityControl.test(value)) return false;
  return value.slice(0, -1).split('/').every((part) =>
    part !== '' && part !== '.' && part !== '..' && part.trim() === part);
}

function readOrCreateProjectAnchorV2(workspaceDir: string): { version: 2; anchor: string; shared: boolean } {
  const anchorPath = resolve(workspaceDir, projectIdentityV2File);
  for (;;) {
    try {
      return decodeProjectAnchorV2(readFileSync(anchorPath, 'utf8'));
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
    }

    const anchor = { version: PROJECT_IDENTITY_VERSION_V2, anchor: randomBytes(16).toString('hex'), shared: false };
    const payload = `${JSON.stringify(anchor, null, 2)}\n`;
    decodeProjectAnchorV2(payload);
    if (publishProjectAnchorV2(anchorPath, payload)) {
      return anchor;
    }
  }
}

function decodeProjectAnchorV2(data: string): { version: 2; anchor: string; shared: boolean } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(data) as unknown;
  } catch {
    invalidAnchorFile();
  }
  const keys = parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? Object.keys(parsed).sort() : [];
  const anchor = parsed as { version?: number; anchor?: string; shared?: boolean };
  if (keys.length !== projectAnchorV2Keys.length || keys.some((key, index) => key !== projectAnchorV2Keys[index]) ||
      anchor.version !== PROJECT_IDENTITY_VERSION_V2 || typeof anchor.anchor !== 'string' || !strictAnchorV2.test(anchor.anchor) || typeof anchor.shared !== 'boolean') {
    invalidAnchorFile();
  }
  return { version: 2, anchor: anchor.anchor, shared: anchor.shared };
}

function publishProjectAnchorV2(anchorPath: string, payload: string): boolean {
  const tempPath = `${anchorPath}.tmp-${process.pid}-${randomBytes(16).toString('hex')}`;
  let descriptor: number | undefined;
  let phase: 'create' | 'write' | 'sync' | 'close' | 'publish' = 'create';
  let primaryError: unknown;
  try {
    descriptor = openSync(tempPath, 'wx', 0o600);
    phase = 'write';
    writeFileSync(descriptor, payload, 'utf8');
    phase = 'sync';
    fsyncSync(descriptor);
    phase = 'close';
    closeSync(descriptor);
    descriptor = undefined;
    phase = 'publish';
    // Hard-link publication is atomic and refuses to replace an existing name.
    linkSync(tempPath, anchorPath);
  } catch (error) {
    primaryError = error;
  }

  if (descriptor === undefined && phase === 'create' && primaryError) throw primaryError;
  let closeError: unknown;
  if (descriptor !== undefined) {
    try { closeSync(descriptor); } catch (error) { closeError = error; }
  }
  let cleanupError: unknown;
  try { unlinkSync(tempPath); } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') cleanupError = error;
  }
  if (cleanupError) throw projectAnchorPublicationError(primaryError, closeError, cleanupError);
  if (primaryError) {
    if (phase === 'publish' && (primaryError as NodeJS.ErrnoException).code === 'EEXIST' && !closeError) return false;
    throw projectAnchorPublicationError(primaryError, closeError);
  }
  if (closeError) throw projectAnchorPublicationError(closeError);
  return true;
}

function projectAnchorPublicationError(...errors: unknown[]): Error {
  const present = errors.filter((error) => error != null);
  if (present.length === 1 && present[0] instanceof Error) return present[0];
  return new Error(present.map((error) => error instanceof Error ? error.message : String(error)).join('; '));
}

function invalidAnchorFile(): never {
  throw new Error(`PROJECT_IDENTITY_INVALID: malformed ${projectIdentityV2File}`);
}

// ---------------------------------------------------------------------------
// Internal helpers (ported from plugin/engram/hooks/lib.js)
// ---------------------------------------------------------------------------

interface GitRemoteResult {
  projectId: string;
  gitRemote: string;
  relativePath: string;
}

/**
 * Compute a stable project ID from the git remote origin URL and relative path.
 * Returns null if the directory is not a git repository or has no remote.
 */
function getGitRemoteID(cwd: string): GitRemoteResult | null {
  const cacheKey = resolve(cwd);
  if (gitRemoteCache.has(cacheKey)) return gitRemoteCache.get(cacheKey)!;

  try {
    const opts = {
      cwd,
      stdio: ['ignore', 'pipe', 'pipe'] as ['ignore', 'pipe', 'pipe'],
      timeout: 3000,
      env: { ...process.env, LC_ALL: 'C', LANG: 'C' },
    };
    const remoteURL = execSync('git remote get-url origin', opts).toString().trim();
    if (!remoteURL) {
      gitRemoteCache.set(cacheKey, null);
      return null;
    }
    const relativePath = execSync('git rev-parse --show-prefix', opts).toString().trim();
    const key = remoteURL + '/' + relativePath;
    const hash = createHash('sha256').update(key).digest('hex');
    // Derive repo name from remote URL so projectId is stable across checkouts
    const repoName =
      remoteURL.replace(/\/$/, '').split('/').pop()?.replace(/\.git$/, '') || 'repo';
    const result: GitRemoteResult = {
      projectId: repoName + '_' + hash.slice(0, 8),
      gitRemote: remoteURL,
      relativePath,
    };
    gitRemoteCache.set(cacheKey, result);
    return result;
  } catch (error) {
    if (isMissingGitIdentityError(error)) {
      gitRemoteCache.set(cacheKey, null);
      return null;
    }
    throw new Error('PROJECT_IDENTITY_UNAVAILABLE: git identity resolution failed', { cause: error });
  }
}

function isMissingGitIdentityError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false;
  const stderr = (error as { stderr?: unknown }).stderr;
  return /not a git repository|no such remote/i.test(stderr == null ? '' : String(stderr));
}

/**
 * Legacy path-based project ID (6-char hash of absolute path).
 * Used as a fallback for directories without a git remote.
 */
function legacyProjectID(cwd: string): string {
  const resolvedPath = resolve(cwd);
  const dirName = basename(resolvedPath);
  const hash = createHash('sha256').update(resolvedPath).digest('hex');
  return dirName + '_' + hash.slice(0, 6);
}

/**
 * Compute the canonical project ID for the given working directory.
 * Prefers git-remote-based ID; falls back to path-based ID.
 */
export function projectIDFromWorkspace(workspaceDir: string): string {
  const gitResult = getGitRemoteID(workspaceDir);
  return gitResult ? gitResult.projectId : legacyProjectID(workspaceDir);
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Resolve the full project identity for an agent session.
 *
 * @param agentId    - The unique agent session ID from OpenClaw (primary scope).
 * @param workspaceDir - Optional workspace directory for git-based ID resolution.
 * @returns          A ProjectIdentity with the resolved projectId.
 */
export function resolveIdentity(
  agentId: string,
  workspaceDir?: string,
): ProjectIdentity {
  // agentId-first: when no workspace directory is available, use agentId as scope
  if (!workspaceDir) {
    return { projectId: agentId, agentId };
  }

  const gitResult = getGitRemoteID(workspaceDir);
  if (gitResult) {
    const projectIdentityV2 = validateProjectIdentityV2(buildProjectIdentityV2({
      legacy_project_id: legacyProjectID(workspaceDir),
      display_name: basename(resolve(workspaceDir)),
      git_remote: gitResult.gitRemote,
      relative_path: gitResult.relativePath.replace(/\\/g, '/'),
    }));
    return {
      projectId: gitResult.projectId,
      agentId,
      gitRemote: gitResult.gitRemote,
      relativePath: gitResult.relativePath,
      projectIdentityV2,
    };
  }

  const anchor = readOrCreateProjectAnchorV2(workspaceDir);
  return {
    projectId: legacyProjectID(workspaceDir),
    agentId,
    projectIdentityV2: validateProjectIdentityV2(buildProjectIdentityV2({
      legacy_project_id: legacyProjectID(workspaceDir),
      display_name: basename(resolve(workspaceDir)),
      non_git_anchor: anchor.anchor,
      anchor_shared: anchor.shared,
    })),
  };
}
