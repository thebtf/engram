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

import { createHash } from 'node:crypto';
import { execSync } from 'node:child_process';
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
    return {
      projectId: gitResult.projectId,
      agentId,
      gitRemote: gitResult.gitRemote,
      relativePath: gitResult.relativePath,
    };
  }

  return {
    projectId: legacyProjectID(workspaceDir),
    agentId,
  };
}
