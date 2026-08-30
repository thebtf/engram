import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import os from 'node:os';
import net from 'node:net';
import path from 'node:path';
import test from 'node:test';
import {
  ADAPTER_REVISION,
  INSTALLED_ARTIFACT_DIGEST_ALGORITHM,
  calculateInstalledArtifactSha256,
  createLegacyRelay,
  goUserCacheDir,
  legacyRelayDialEndpoint,
  parseLegacyRelayLocator,
  RELAY_PROTOCOL,
} from './legacy-relay.mjs';

const artifactDigest = 'a'.repeat(64);
const capability = Buffer.alloc(32, 9).toString('base64url');
const descriptor = Object.freeze({
  version: 3,
  anchor_project_id: '33333333-3333-4333-8333-333333333333',
  name: 'engram',
  scope: 'repository',
  normalized_git_remotes: ['github.com/thebtf/engram'],
  legacy_identifiers: [],
  client_instance_id: 'relay-helper-test-client',
});

function writeLocator(file, generation, endpoint) {
  fs.writeFileSync(file, `${JSON.stringify({
    protocol: RELAY_PROTOCOL,
    daemon_generation: generation,
    endpoint,
  })}\n`);
}

function response(request, extra) {
  return JSON.stringify({
    protocol: RELAY_PROTOCOL,
    requestId: request.requestId,
    daemonGeneration: request.daemonGeneration,
    route: request.route,
    ...extra,
  });
}

class FakeSocket extends EventEmitter {
  constructor(endpoint, responder) {
    super();
    this.endpoint = endpoint;
    this.responder = responder;
    queueMicrotask(() => this.emit('connect'));
  }

  end(frame) {
    let projected;
    try {
      projected = this.responder(Buffer.from(frame), this.endpoint);
    } catch (error) {
      queueMicrotask(() => this.emit('error', error));
      return;
    }
    queueMicrotask(() => {
      const chunks = Array.isArray(projected) ? projected : [projected];
      for (const chunk of chunks) this.emit('data', Buffer.from(chunk));
      this.emit('end');
      this.emit('close', false);
    });
  }

  destroy() {
    this.destroyed = true;
  }
}

function fakeNet(responder, endpoints = []) {
  return {
    createConnection({ path: endpoint }) {
      endpoints.push(endpoint);
      return new FakeSocket(endpoint, responder);
    },
  };
}

function identityBody() {
  return { hostSessionRef: 'host-session-ref', projectIdentityV3: descriptor };
}

test('user cache resolution mirrors Go UserCacheDir without fallback drift', () => {
  assert.equal(goUserCacheDir({ platform: 'win32', env: { LOCALAPPDATA: 'C:\\Users\\Test\\AppData\\Local' } }), 'C:\\Users\\Test\\AppData\\Local');
  assert.equal(goUserCacheDir({ platform: 'darwin', env: { HOME: '/Users/test' } }), '/Users/test/Library/Caches');
  assert.equal(goUserCacheDir({ platform: 'linux', env: { XDG_CACHE_HOME: '/var/cache/test' } }), '/var/cache/test');
  assert.equal(goUserCacheDir({ platform: 'linux', env: { XDG_CACHE_HOME: 'relative', HOME: '/home/test' } }), '');
  assert.equal(goUserCacheDir({ platform: 'linux', env: { HOME: '/home/test' } }), '/home/test/.cache');
});

test('Windows logical muxcore paths map to the exact mcp-mux named pipe', () => {
  const logical = 'C:\\Users\\Test\\AppData\\Local\\engram\\run\\hap-01b\\engram.sock';
  assert.equal(
    legacyRelayDialEndpoint(logical, 'win32'),
    '\\\\.\\pipe\\mcp-mux-74c8ea331a7ca72f303a8b643a570e90',
  );
  assert.equal(legacyRelayDialEndpoint('\\\\.\\pipe\\already-physical', 'win32'), '');
  assert.equal(legacyRelayDialEndpoint('/home/test/.cache/engram/relay.sock', 'linux'), '/home/test/.cache/engram/relay.sock');
});


test('real Windows named-pipe relay round trip uses the muxcore logical-path mapping', { skip: process.platform !== 'win32' }, async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-relay-real-'));
  const locator = path.join(directory, 'locator.json');
  const logical = path.join(directory, 'engram.sock');
  const pipe = legacyRelayDialEndpoint(logical, 'win32');
  writeLocator(locator, 'real-daemon-generation', logical);

  const server = net.createServer((socket) => {
    let requestBytes = Buffer.alloc(0);
    socket.on('data', (chunk) => {
      requestBytes = Buffer.concat([requestBytes, chunk]);
      const newline = requestBytes.indexOf(0x0a);
      if (newline < 0) return;
      const request = JSON.parse(requestBytes.subarray(0, newline));
      socket.end(`${response(request, {
        kind: 'OK',
        sessionCapability: capability,
        canonicalProjectRef: 'canonical-project-ref',
      })}\n`);
    });
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(pipe, resolve);
  });
  t.after(async () => {
    await new Promise((resolve) => server.close(resolve));
    fs.rmSync(directory, { recursive: true, force: true });
  });

  const relay = createLegacyRelay({
    platform: 'win32',
    locatorPath: () => locator,
    artifactDigest,
  });
  const result = await relay.call('IDENTITY_REGISTRATION', identityBody(), Date.now() + 2_000);
  assert.equal(result.kind, 'OK');
  assert.equal(result.route, 'IDENTITY_REGISTRATION');
});
test('locator parsing rejects duplicate fields and accepts the Go logical endpoint', () => {
  const logical = 'C:\\Users\\Test\\AppData\\Local\\engram\\run\\hap-01b\\engram.sock';
  const accepted = parseLegacyRelayLocator(Buffer.from(JSON.stringify({
    protocol: RELAY_PROTOCOL,
    daemon_generation: 'daemon-generation',
    endpoint: logical,
  })), { platform: 'win32' });
  assert.deepEqual(accepted, {
    protocol: RELAY_PROTOCOL,
    daemonGeneration: 'daemon-generation',
    endpoint: logical,
  });
  assert.equal(parseLegacyRelayLocator(Buffer.from(`{"protocol":"${RELAY_PROTOCOL}","protocol":"${RELAY_PROTOCOL}","daemon_generation":"daemon-generation","endpoint":"${logical.replaceAll('\\', '\\\\')}"}`), { platform: 'win32' }), null);
});

test('two-file adapter digest is deterministic, path-stable, and byte-sensitive', (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-relay-digest-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const entry = path.join(directory, 'engram-memory.mjs');
  const helper = path.join(directory, 'legacy-relay.mjs');
  fs.writeFileSync(entry, 'entry bytes');
  fs.writeFileSync(helper, 'helper bytes');
  const files = [
    { relativePath: 'extensions/engram-memory.mjs', filePath: entry },
    { relativePath: 'extensions/legacy-relay.mjs', filePath: helper },
  ];
  const first = calculateInstalledArtifactSha256(files);
  const reversed = calculateInstalledArtifactSha256([...files].reverse());
  assert.match(INSTALLED_ARTIFACT_DIGEST_ALGORITHM, /u32be.*u64be/);
  assert.match(first, /^[a-f0-9]{64}$/);
  assert.equal(reversed, first);
  fs.writeFileSync(helper, 'helper bytes changed');
  assert.notEqual(calculateInstalledArtifactSha256(files), first);
});

test('relay emits one exact frame, maps the endpoint, and caches the adapter digest', async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-relay-call-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const locator = path.join(directory, 'locator.json');
  const logical = 'C:\\Users\\Test\\AppData\\Local\\engram\\run\\hap-01b\\engram.sock';
  writeLocator(locator, 'daemon-generation', logical);
  const requests = [];
  const endpoints = [];
  let digestCalls = 0;
  const relay = createLegacyRelay({
    platform: 'win32',
    locatorPath: () => locator,
    artifactDigest: () => { digestCalls += 1; return artifactDigest; },
    randomBytes: () => Buffer.alloc(16, 1),
    now: () => 1_000,
    net: fakeNet((frame) => {
      assert.equal(frame.at(-1), 0x0a);
      const request = JSON.parse(frame.subarray(0, -1));
      requests.push(request);
      return `${response(request, {
        kind: 'OK',
        sessionCapability: capability,
        canonicalProjectRef: 'canonical-project-ref',
      })}\n`;
    }, endpoints),
  });

  const first = await relay.call('IDENTITY_REGISTRATION', identityBody(), 5_000);
  const second = await relay.call('IDENTITY_REGISTRATION', identityBody(), 5_000);
  assert.equal(first.kind, 'OK');
  assert.equal(second.kind, 'OK');
  assert.equal(digestCalls, 1);
  assert.deepEqual(endpoints, [
    '\\\\.\\pipe\\mcp-mux-74c8ea331a7ca72f303a8b643a570e90',
    '\\\\.\\pipe\\mcp-mux-74c8ea331a7ca72f303a8b643a570e90',
  ]);
  assert.equal(requests.length, 2);
  for (const request of requests) {
    assert.equal(request.protocol, RELAY_PROTOCOL);
    assert.equal(request.daemonGeneration, 'daemon-generation');
    assert.deepEqual(request.adapter, {
      revision: ADAPTER_REVISION,
      installedArtifactSha256: artifactDigest,
    });
    assert.equal(request.route, 'IDENTITY_REGISTRATION');
    assert.equal(request.deadlineUnixMs, 5_000);
    assert.deepEqual(request.body, identityBody());
  }
});

test('stale generation permits exactly one locator rediscovery within the original deadline', async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-relay-stale-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const locator = path.join(directory, 'locator.json');
  writeLocator(locator, 'generation-one', '/tmp/relay-one.sock');
  const generations = [];
  const endpoints = [];
  const relay = createLegacyRelay({
    platform: 'linux',
    locatorPath: () => locator,
    artifactDigest,
    randomBytes: () => Buffer.alloc(16, 2),
    now: () => 1_000,
    net: fakeNet((frame) => {
      const request = JSON.parse(frame.subarray(0, -1));
      generations.push(request.daemonGeneration);
      if (generations.length === 1) {
        writeLocator(locator, 'generation-two', '/tmp/relay-two.sock');
        return `${response(request, { kind: 'NO_DELIVERY', reason: 'STALE_GENERATION' })}\n`;
      }
      return `${response(request, {
        kind: 'OK',
        sessionCapability: capability,
        canonicalProjectRef: 'canonical-project-ref',
      })}\n`;
    }, endpoints),
  });

  const result = await relay.call('IDENTITY_REGISTRATION', identityBody(), 5_000);
  assert.equal(result.kind, 'OK');
  assert.deepEqual(generations, ['generation-one', 'generation-two']);
  assert.deepEqual(endpoints, ['/tmp/relay-one.sock', '/tmp/relay-two.sock']);
});

test('trailing response bytes and elapsed deadlines fail closed without a second dispatch', async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-relay-trailing-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const locator = path.join(directory, 'locator.json');
  writeLocator(locator, 'daemon-generation', '/tmp/relay.sock');
  let calls = 0;
  const relay = createLegacyRelay({
    platform: 'linux',
    locatorPath: () => locator,
    artifactDigest,
    randomBytes: () => Buffer.alloc(16, 3),
    now: () => 1_000,
    net: fakeNet((frame) => {
      calls += 1;
      const request = JSON.parse(frame.subarray(0, -1));
      return `${response(request, {
        kind: 'OK',
        sessionCapability: capability,
        canonicalProjectRef: 'canonical-project-ref',
      })}\ntrailing`;
    }),
  });
  assert.deepEqual(await relay.call('IDENTITY_REGISTRATION', identityBody(), 5_000), {
    kind: 'NO_DELIVERY',
    route: 'IDENTITY_REGISTRATION',
    reason: 'SERVER_UNAVAILABLE',
  });
  assert.equal(calls, 1);
  assert.equal((await relay.call('IDENTITY_REGISTRATION', identityBody(), 1_000)).reason, 'DEADLINE_ELAPSED');
  assert.equal(calls, 1);
});
