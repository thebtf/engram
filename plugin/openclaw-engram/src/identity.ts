import { execFile } from 'node:child_process';

export interface ProjectIdentity {
  projectId: string;
  agentId: string;
  name?: string;
}

export function validateProjectID(value: unknown): string {
  if (typeof value !== 'string' || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value) || /^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(value)) {
    throw new Error('PROJECT_IDENTITY_INVALID');
  }
  return value;
}

/** The Go client owns marker, common-dir and bootstrap resolution. */
export async function resolveIdentity(
  agentId: string,
  workspaceDir?: string,
  options: { env?: NodeJS.ProcessEnv; signal?: AbortSignal; timeoutMs?: number } = {},
): Promise<ProjectIdentity> {
  if (!workspaceDir) throw new Error('PROJECT_CONTEXT_REQUIRED');
  const env = options.env ?? process.env;
  const raw = await new Promise<string>((resolve, reject) => {
    execFile(env.ENGRAM_EXECUTABLE || 'engram', ['project', 'resolve', '--root', workspaceDir, '--json'], {
      env, encoding: 'utf8', windowsHide: true, timeout: options.timeoutMs ?? 15000,
      maxBuffer: 4096, signal: options.signal,
    }, (error, stdout) => {
      if (error) {
        let code = 'PROJECT_RESOLUTION_UNAVAILABLE';
        let guidance = '';
        try {
          const result: unknown = JSON.parse(stdout);
          if (result && typeof result === 'object' && 'code' in result && typeof result.code === 'string' && /^PROJECT_[A-Z_]+$/.test(result.code)) code = result.code;
          if (result && typeof result === 'object' && 'guidance' in result && typeof result.guidance === 'string' && result.guidance.length <= 3072) guidance = result.guidance;
        } catch { /* No valid structured error from the required CLI interface. */ }
        reject(Object.assign(new Error(code), { guidance }));
      } else resolve(stdout);
    });
  });
  const result: unknown = JSON.parse(raw);
  if (!result || typeof result !== 'object' || !('project_id' in result)) throw new Error('PROJECT_IDENTITY_INVALID');
  const projectId = validateProjectID(result.project_id);
  if (!('name' in result)) return { projectId, agentId };
  if (typeof result.name !== 'string') throw new Error('PROJECT_IDENTITY_INVALID');
  return { projectId, agentId, name: result.name };
}
