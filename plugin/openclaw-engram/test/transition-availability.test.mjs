import assert from 'node:assert/strict';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED || fileURLToPath(new URL('../dist/', import.meta.url));
const { EngramRestClient } = await import(pathToFileURL(path.join(compiled, 'client.js')));
const project = 'abcdefab-cdef-4abc-8def-abcdefabcdef';
const memory = { project, title: 'Synthetic availability note', content: 'Retain unrelated valid writes', type: 'change' };

async function fixture(t, reply, timeoutMs = 1000) {
  const requests = [];
  const server = http.createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : undefined;
    requests.push({ method: request.method, path: request.url, body });
    reply(request, response, body);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const client = new EngramRestClient({ url: `http://127.0.0.1:${server.address().port}`, token: 'synthetic-http-fixture', project, timeoutMs });
  return { client, requests };
}

function confirm(response, body) {
  response.writeHead(201, { 'Content-Type': 'application/json' });
  response.end(JSON.stringify({ id: 41, project: body.project, content: body.content }));
}

test('three real HTTP transition conflicts remain rejected without blocking an unrelated store', async t => {
  const { client, requests } = await fixture(t, (request, response, body) => {
    if (request.url.includes('/transition')) { response.writeHead(409); response.end('synthetic confirmed rollback'); }
    else confirm(response, body);
  });
  for (let i = 0; i < 3; i++) {
    const result = await client.transitionMemory(41, 'suppress', project);
    assert.equal(result.outcome, 'rejected');
    assert.match(result.error, /HTTP 409/);
    assert.equal(client.isAvailable(), true, 'a business conflict is neither a transport strike nor cooldown');
  }
  const [stored] = await client.storeMemories([memory]);
  assert.equal(stored.ok, true, JSON.stringify(stored));
  assert.equal(requests.length, 4, 'one request per input, no blind transition retry');
  assert.equal(requests.at(-1).path, '/api/memories');
});

test('normal transition conflict preserves prior genuine failure history and exact cooldown recovery', async t => {
  let now = Date.now();
  t.mock.method(Date, 'now', () => now);
  const { client, requests } = await fixture(t, (request, response, body) => {
    if (request.url.includes('/transition')) { response.writeHead(409); response.end('synthetic confirmed rollback'); }
    else if (request.url === '/api/health') { response.writeHead(503); response.end('synthetic service failure'); }
    else confirm(response, body);
  });
  await client.health();
  await client.health();
  assert.equal(client.isAvailable(), true);
  assert.equal((await client.transitionMemory(41, 'suppress', project)).outcome, 'rejected');
  assert.equal(client.isAvailable(), true, '409 must not add a third strike');
  await client.health();
  assert.equal(client.isAvailable(), false, '409 must not reset the two genuine prior failures');
  assert.equal((await client.storeMemories([memory]))[0].outcome, 'not_sent');
  now += 59_999;
  assert.equal((await client.storeMemories([memory]))[0].outcome, 'not_sent');
  assert.equal(requests.length, 4);
  now++;
  assert.equal((await client.storeMemories([memory]))[0].ok, true);
  assert.equal(client.isAvailable(), true);
  assert.equal(requests.length, 5);
});

test('genuine HTTP timeouts retain unknown outcomes, three strikes and no blind retry', async t => {
  const { client, requests } = await fixture(t, () => { }, 200);
  for (let i = 0; i < 3; i++) {
    const [result] = await client.storeMemories([memory]);
    assert.equal(result.outcome, 'unknown');
    assert.match(result.error, /abort|inspect the outcome/i);
  }
  assert.equal(client.isAvailable(), false);
  assert.equal((await client.storeMemories([memory]))[0].outcome, 'not_sent');
  assert.equal(requests.length, 3, 'each timed-out POST is sent once and never retried');
});

test('HTTP409 outside the retained-transition contract keeps its existing availability semantics', async t => {
  const { client, requests } = await fixture(t, (_request, response) => { response.writeHead(409); response.end('synthetic other endpoint failure'); });
  for (let i = 0; i < 3; i++) assert.equal(await client.health(), null);
  assert.equal(client.isAvailable(), false);
  assert.equal((await client.storeMemories([memory]))[0].outcome, 'not_sent');
  assert.equal(requests.length, 3);
});
