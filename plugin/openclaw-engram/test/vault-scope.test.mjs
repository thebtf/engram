import assert from 'node:assert/strict';
import { randomBytes, randomUUID } from 'node:crypto';
import fs from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
const clientModule = await import(compiled ? pathToFileURL(path.join(compiled, 'client.js')).href : '../dist/client.js');
const toolModule = await import(compiled ? pathToFileURL(path.join(compiled, 'tools/engram-vault.js')).href : '../dist/tools/engram-vault.js');

test('Vault client and explicit global tool preserve exact namespace selectors', async () => {
  const privateDir = await fs.mkdtemp(path.join(os.tmpdir(), 'vault-scope-'));
  const project = randomUUID();
  const value = randomBytes(24).toString('hex');
  await fs.writeFile(path.join(privateDir, 'value'), value, { mode: 0o600 });
  const requests = [];
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    requests.push(url);
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ name: 'same/name', value, scope: url.searchParams.get('scope'), project_id: url.searchParams.get('project'), id: 1 }));
  });
  try {
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    const client = new clientModule.EngramRestClient({ url: `http://127.0.0.1:${server.address().port}`, token: '', timeoutMs: 1000 });
    const projectResult = await client.getCredential('same/name', { scope: 'project', project });
    assert.equal(projectResult.project_id, project);
    assert.equal(requests[0].pathname, '/api/vault/credentials/same%2Fname');
    assert.equal(requests[0].searchParams.get('scope'), 'project');
    assert.equal(requests[0].searchParams.get('project'), project);
    const globalResult = await client.getCredential('same/name', { scope: 'global' });
    assert.equal(globalResult.project_id, null);
    assert.equal(requests[1].searchParams.get('scope'), 'global');
    assert.equal(requests[1].searchParams.has('project'), false);
    const tool = toolModule.createEngramVaultGetTool({}, client, {});
    const result = await tool.execute('global-scope-check', { name: 'same/name', scope: 'global' });
    assert.ok(result.includes('Engram credential record'));
    assert.equal(requests.length, 3);
    assert.equal(requests[2].searchParams.get('scope'), 'global');
    assert.equal(requests[2].searchParams.has('project'), false);
  } finally {
    await new Promise((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));
    await fs.rm(privateDir, { recursive: true, force: true });
  }
});
