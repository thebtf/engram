import { execFile } from 'node:child_process';
import { z } from 'zod';

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

const ProjectSelectionReferenceSchema = z.string().regex(/^engram-project-choice-v1:[0-9a-f]{64}$/);

export const ProjectContextRequestSchema = z.discriminatedUnion('action', [
  z.object({ action: z.literal('choices') }).strict(),
  z.object({ action: z.literal('select'), selection_ref: ProjectSelectionReferenceSchema }).strict(),
]);

export type ProjectContextRequest = z.infer<typeof ProjectContextRequestSchema>;

const ContextProjectIDSchema = z.string().refine((value) => {
  try { return validateProjectID(value) === value; } catch { return false; }
});
const ProjectNameSchema = z.string().max(512).refine((value) => !/\p{Cc}/u.test(value));
const RepositoryQualifierSchema = z.string().max(2048).refine((value) => {
  if (value === '') return true;
  if (value.trim() !== value || value.startsWith('/') || /[\\?#\p{Cc}]/u.test(value)) return false;
  try {
    const url = new URL('https://' + value);
    return url.hostname !== '' && url.username === '' && url.password === '' && url.pathname !== '/' && url.search === '' && url.hash === '';
  } catch { return false; }
});
const ProjectChoiceSchema = z.object({
  project_id: ContextProjectIDSchema,
  name: ProjectNameSchema.min(1),
  repository_qualifier: RepositoryQualifierSchema.optional(),
  selection_ref: ProjectSelectionReferenceSchema,
});
const ProjectChoicesSchema = z.object({ choices: z.array(ProjectChoiceSchema).max(4096) });
const SelectedProjectSchema = z.object({ project_id: ContextProjectIDSchema, name: ProjectNameSchema.optional() });
const ProjectCommandFailureSchema = z.object({ code: z.string().regex(/^PROJECT_[A-Z_]{1,80}$/) });

export type ProjectContextResult =
  | { ok: true; action: 'choices'; choices: z.infer<typeof ProjectChoicesSchema>['choices'] }
  | { ok: true; action: 'select'; project: z.infer<typeof SelectedProjectSchema> }
  | { ok: false; code: string; outcome: 'not_sent' | 'rejected' | 'unknown' };

export function projectContextGuidance(code: string): string {
  if (code === 'PROJECT_CONTEXT_REQUIRED') {
    return 'The host must supply the actual caller workspace before engram_project_context choices/select can run. Do not substitute cwd, configured workspace, another project or a session ID. Keep the original operation held and its arguments/session unchanged.';
  }
  if (code === 'PROJECT_RESOLUTION_UNAVAILABLE') {
    return 'Keep the original operation held, preserving its target, actor, session and client. Restore the server connection and caller authorization, or local storage access, as applicable; do not repeat choices or select while the dependency remains unavailable. Once access is restored, if an earlier select has an unconfirmed outcome, inspect the existing caller binding or selection outcome before retrying selection. The owner can inspect the actual caller’s regular identity files with bounded reads (4096 bytes maximum per file); do not follow symlinks or expose private inputs. If that unconfirmed selection outcome cannot be established safely, keep the operation held for authorized owner inspection. For an established binding only, the installed native client or plugin launcher can revalidate it with engram project resolve --root <actual-caller-root> --json using the same caller instance, root, server and authorization. Without an established binding ordinary resolve may bootstrap or persist identity, so do not use it merely to inspect an unconfirmed selection. If no select was attempted or a select is known not sent, use fresh authorized choices when a genuine choice is required. If an earlier selection outcome is established and a genuine choice is still required, use engram_project_context action="choices" and explicitly select the intended existing project with its exact fresh authorized selection_ref; never pick first/latest or by name alone. Inspect any unknown write outcome before retrying it; never replay an unconfirmed mutation. Preserve marker, cache, data and permissions; do not invent identity or substitute another caller.';
  }
  if (['PROJECT_MIGRATION_REQUIRED', 'PROJECT_IDENTITY_CONFLICT', 'PROJECT_SELECTION_INVALID', 'PROJECT_SELECTION_STALE'].includes(code)) {
    return 'Use engram_project_context with action="choices" in this same caller workspace. Review the fresh authorized names, full project IDs and repository qualifiers. Choose the intended existing project explicitly, never first/latest, by name alone or merely because one result exists. Call engram_project_context with action="select" and that exact fresh selection_ref. After confirmed selection, retry the original operation unchanged with its original target, actor, session and client. Do not create a replacement project or alter marker/cache/data/permissions. Recovery does not prove an earlier mutation failed: inspect any unknown write outcome before retrying it.';
  }
  return 'Preserve the actual caller marker, common cache, v2 anchor and data. Invalid identity or an unknown refusal requires bounded regular-file owner inspection (4096 bytes maximum) and authorized repair. Do not follow symlinks, execute marker content, borrow another identity, delete data or initialize a replacement project. Keep the original operation held; do not loop the same failed check or treat refusal as an empty successful result.';
}

/** The native CLI owns fresh catalog authorization and caller-scoped selection storage. */
export async function requestProjectContext(
  request: ProjectContextRequest,
  workspaceDir: string | undefined,
  options: { env?: NodeJS.ProcessEnv; signal?: AbortSignal; timeoutMs?: number } = {},
): Promise<ProjectContextResult> {
  if (typeof workspaceDir !== 'string' || workspaceDir.length === 0) {
    return { ok: false, code: 'PROJECT_CONTEXT_REQUIRED', outcome: 'not_sent' };
  }
  const env = options.env ?? process.env;
  const args = ['project', request.action, '--root', workspaceDir, '--json'];
  if (request.action === 'select') args.push('--selection-ref', request.selection_ref);
  const exposesCallerValue = (value: string | undefined): boolean => value !== undefined &&
    (value.includes(workspaceDir) || Boolean(env.ENGRAM_TOKEN && value.includes(env.ENGRAM_TOKEN)));
  const unconfirmedOutcome = request.action === 'select' ? 'unknown' : 'rejected';
  try {
    return await new Promise<ProjectContextResult>((resolve) => {
      execFile(env.ENGRAM_EXECUTABLE || 'engram', args, {
        env, encoding: 'utf8', windowsHide: true,
        timeout: options.timeoutMs ?? 15000, maxBuffer: 2 * 1024 * 1024, signal: options.signal,
      }, (error, stdout) => {
        let payload: unknown;
        try { payload = JSON.parse(stdout); } catch { payload = undefined; }
        const failure = ProjectCommandFailureSchema.safeParse(payload);
        if (error || failure.success) {
          const didNotStart = error && ['ENOENT', 'EACCES', 'ENOEXEC', 'ENOTDIR'].includes(String(error.code));
          const code = failure.success && !exposesCallerValue(failure.data.code) ? failure.data.code : 'PROJECT_RESOLUTION_UNAVAILABLE';
          resolve({ ok: false, code, outcome: didNotStart ? 'not_sent' : unconfirmedOutcome });
          return;
        }
        if (request.action === 'choices') {
          const parsed = ProjectChoicesSchema.safeParse(payload);
          if (parsed.success && !parsed.data.choices.some((choice) => exposesCallerValue(choice.name) || exposesCallerValue(choice.repository_qualifier))) {
            resolve({ ok: true, action: 'choices', choices: parsed.data.choices });
            return;
          }
        } else {
          const parsed = SelectedProjectSchema.safeParse(payload);
          if (parsed.success && !exposesCallerValue(parsed.data.name)) {
            resolve({ ok: true, action: 'select', project: parsed.data });
            return;
          }
        }
        resolve({ ok: false, code: 'PROJECT_IDENTITY_INVALID', outcome: unconfirmedOutcome });
      });
    });
  } catch {
    return { ok: false, code: 'PROJECT_RESOLUTION_UNAVAILABLE', outcome: 'not_sent' };
  }
}
