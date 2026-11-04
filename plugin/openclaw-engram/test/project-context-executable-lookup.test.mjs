import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const execFileAsync = promisify(execFile);
const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED || fileURLToPath(new URL('../dist/', import.meta.url));
const identityModule = path.resolve(compiled, 'identity.js');
const token = 'synthetic-executable-lookup-token';

async function fixture(t) {
  const parent = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-exec-lookup-'));
  t.after(() => fs.rmSync(parent, { recursive: true, force: true }));
  const workspace = path.join(parent, 'untrusted workspace');
  const receipts = path.join(parent, 'receipts');
  fs.mkdirSync(path.join(parent, 'bin'));
  fs.mkdirSync(path.join(workspace, 'bin'), { recursive: true });
  fs.mkdirSync(receipts);

  for (const [directory, name] of [[parent, 'trusted'], [workspace, 'workspace']]) {
    const executable = path.join(directory, 'bin', 'engram');
    fs.writeFileSync(executable, `#!/bin/sh
if [ "$ENGRAM_TOKEN" = "$FIXTURE_EXPECTED_TOKEN" ]; then
  printf '%s\\n' matched > "$FIXTURE_RECEIPTS/${name}.token"
else
  printf '%s\\n' missing-or-different > "$FIXTURE_RECEIPTS/${name}.token"
fi
printf '%s\\n' "$@" > "$FIXTURE_RECEIPTS/${name}.args"
printf '%s\\n' '{"choices":[]}'
`, { mode: 0o700 });
  }

  const runner = path.join(parent, 'runner.cjs');
  fs.writeFileSync(runner, `
(async () => {
  const imported = await import(require('node:url').pathToFileURL(process.argv[2]).href);
  const requestProjectContext = imported.requestProjectContext ?? imported.default?.requestProjectContext;
  const result = await requestProjectContext({ action: 'choices' }, process.argv[3], { env: process.env });
  process.stdout.write(JSON.stringify(result));
})().catch(() => { process.stderr.write('fixture runner failed'); process.exitCode = 1; });
`);

  async function invoke(executable) {
    const env = {
      PATH: 'bin:/usr/bin:/bin',
      ENGRAM_TOKEN: token,
      FIXTURE_EXPECTED_TOKEN: token,
      FIXTURE_RECEIPTS: receipts,
      ...(executable ? { ENGRAM_EXECUTABLE: executable } : {}),
    };
    const { stdout } = await execFileAsync(process.execPath, [runner, identityModule, workspace], {
      cwd: parent, env, encoding: 'utf8', timeout: 20000,
    });
    return JSON.parse(stdout);
  }

  function observation(name) {
    const tokenPath = path.join(receipts, `${name}.token`);
    const argsPath = path.join(receipts, `${name}.args`);
    return {
      executed: fs.existsSync(tokenPath),
      tokenMatched: fs.existsSync(tokenPath) && fs.readFileSync(tokenPath, 'utf8').trim() === 'matched',
      args: fs.existsSync(argsPath) ? fs.readFileSync(argsPath, 'utf8').trimEnd().split('\n') : null,
    };
  }

  return { parent, workspace, invoke, observation };
}

const linuxOnly = { skip: process.platform !== 'linux' && 'Requires Linux execFile relative-PATH lookup semantics' };

test('project context: absolute executable override bypasses workspace lookup and receives the actual root', linuxOnly, async t => {
  const f = await fixture(t);
  const result = await f.invoke(path.join(f.parent, 'bin', 'engram'));
  assert.deepEqual(result, { ok: true, action: 'choices', choices: [] });
  assert.deepEqual(f.observation('trusted'), {
    executed: true, tokenMatched: true,
    args: ['project', 'choices', '--root', f.workspace, '--json'],
  });
  assert.deepEqual(f.observation('workspace'), { executed: false, tokenMatched: false, args: null });
});

test('project context: relative PATH resolves the executable from the trusted parent, not the workspace', linuxOnly, async t => {
  const f = await fixture(t);
  const result = await f.invoke();
  assert.deepEqual(result, { ok: true, action: 'choices', choices: [] });
  const trusted = f.observation('trusted');
  const workspace = f.observation('workspace');
  assert.deepEqual({ trusted: trusted.executed, workspace: workspace.executed, workspaceReceivedToken: workspace.tokenMatched },
    { trusted: true, workspace: false, workspaceReceivedToken: false },
    'Relative PATH lookup must not execute the workspace impostor or expose the synthetic token to it');
  assert.equal(trusted.tokenMatched, true, 'The trusted executable must receive the synthetic token');
  assert.deepEqual(trusted.args, ['project', 'choices', '--root', f.workspace, '--json']);
});
