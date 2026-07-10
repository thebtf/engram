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
import { closeSync, openSync, readFileSync, writeFileSync } from 'node:fs';
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
const projectIdentityControl = /[\u0000-\u001f\u007f]/;
const projectAnchorV2Keys = ['anchor', 'shared', 'version'];

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
  if (identity.version !== PROJECT_IDENTITY_VERSION_V2) invalid('unsupported version');
  if (identity.legacy_project_id.length > 256 || identity.display_name.length > 256 ||
      identity.legacy_project_id.trim() !== identity.legacy_project_id ||
      projectIdentityControl.test(identity.legacy_project_id) || projectIdentityControl.test(identity.display_name)) {
    invalid('selector or display name too long');
  }
  const hasGit = identity.git_remote !== '' || identity.relative_path !== '';
  const hasAnchor = identity.non_git_anchor !== '' || identity.anchor_shared !== null;
  if (hasGit === hasAnchor) invalid('exactly one identity source is required');
  if (hasGit) {
    if (!identity.git_remote || identity.git_remote.length > 2048 || identity.git_remote.trim() !== identity.git_remote || projectIdentityControl.test(identity.git_remote)) {
      invalid('git_remote is missing or malformed');
    }
    if (identity.relative_path.length > 4096 || identity.relative_path.startsWith('/') || identity.relative_path.includes('\\') || projectIdentityControl.test(identity.relative_path)) {
      invalid('relative_path is not normalized');
    }
    if (identity.relative_path.split('/').some((part) => part === '.' || part === '..')) invalid('relative_path contains traversal');
  } else if (!strictAnchorV2.test(identity.non_git_anchor) || typeof identity.anchor_shared !== 'boolean') {
    invalid('non-git anchor must be 128-bit lowercase hex with explicit sharing');
  }
  return identity;
}

function readOrCreateProjectAnchorV2(workspaceDir: string): { version: 2; anchor: string; shared: boolean } {
  const anchorPath = resolve(workspaceDir, projectIdentityV2File);
  for (;;) {
    try {
      const parsed = JSON.parse(readFileSync(anchorPath, 'utf8')) as unknown;
      const keys = parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? Object.keys(parsed).sort() : [];
      const anchor = parsed as { version?: number; anchor?: string; shared?: boolean };
      if (keys.length !== projectAnchorV2Keys.length || keys.some((key, index) => key !== projectAnchorV2Keys[index]) ||
          anchor.version !== PROJECT_IDENTITY_VERSION_V2 || typeof anchor.anchor !== 'string' || !strictAnchorV2.test(anchor.anchor) || typeof anchor.shared !== 'boolean') {
        invalidAnchorFile();
      }
      return { version: 2, anchor: anchor.anchor, shared: anchor.shared };
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
    }

    const anchor = { version: PROJECT_IDENTITY_VERSION_V2, anchor: randomBytes(16).toString('hex'), shared: false };
    let descriptor: number | undefined;
    try {
      descriptor = openSync(anchorPath, 'wx', 0o600);
      writeFileSync(descriptor, `${JSON.stringify(anchor, null, 2)}\n`, 'utf8');
      closeSync(descriptor);
      return anchor;
    } catch (error) {
      if (descriptor !== undefined) {
        try { closeSync(descriptor); } catch { /* best effort */ }
      }
      if ((error as NodeJS.ErrnoException).code === 'EEXIST') continue;
      throw error;
    }
  }
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
      stdio: ['ignore', 'pipe', 'ignore'] as ['ignore', 'pipe', 'ignore'],
      timeout: 3000,
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
  } catch {
    gitRemoteCache.set(cacheKey, null);
    return null;
  }
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
