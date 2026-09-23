// Real deployed transport revocation for the explicitly disposable Bob fixture.
// Opens PCM, input and noVNC WebSockets with two independent login sessions,
// logs out one, and verifies only its established transports are revoked.
// No keyboard, mouse, clipboard, RFB input or browser automation is performed.
// Does not access Alice, change passwords/status, or restart any containers.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { mkdir, readFile, writeFile } from 'node:fs/promises';

const expected = {run: 'mu537vjz', id: 'sess_8685794da70b396a', owner: 3, email: 'e2e-bob-mu537vjz@example.test'};
const report = {startedAt: new Date().toISOString(), result: 'failed', scope: 'Real TCP/WebSocket revocation, not browser input, video decoding, or audible playback', transports: []};
const sockets = [], clients = [];
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
let phase = 'validate-disposable-target', logoutStarted;
const inspect = () => JSON.parse(execFileSync('docker', ['inspect', 'rb-sess-' + expected.id], {encoding: 'utf8', timeout: 15000, stdio: ['ignore', 'pipe', 'pipe']}))[0];

function controlFrame(opcode, payload = Buffer.alloc(0)) {
  assert(payload.length <= 125, 'oversized control payload');
  const mask = randomBytes(4), frame = Buffer.alloc(6 + payload.length);
  frame[0] = 0x80 | opcode;
  frame[1] = 0x80 | payload.length;
  mask.copy(frame, 2);
  for (let i = 0; i < payload.length; i++) frame[6 + i] = payload[i] ^ mask[i % 4];
  return frame;
}

function client(base, label) {
  const cookies = new Map();
  const c = {label, loggedIn: false, session: () => cookies.get('rb_session'), cookie: () => [...cookies].map(([k, v]) => k + '=' + v).join('; ')};
  c.api = async (path, method = 'GET', form) => {
    const headers = {Accept: 'application/json', Cookie: c.cookie()};
    if (method !== 'GET') Object.assign(headers, {'X-CSRF-Token': cookies.get('rb_csrf') || '', Origin: base.origin});
    const response = await fetch(new URL(path, base), {method, headers, body: form ? new URLSearchParams(form) : undefined, redirect: 'manual', signal: AbortSignal.timeout(10000)});
    for (const raw of response.headers.getSetCookie()) {
      const pair = raw.split(';', 1)[0], equals = pair.indexOf('=');
      if (equals > 0) cookies.set(pair.slice(0, equals), pair.slice(equals + 1));
    }
    const text = await response.text();
    let data; try { data = JSON.parse(text); } catch {}
    return {status: response.status, data};
  };
  c.login = async () => {
    assert.equal((await c.api('/login')).status, 200, label + ' login page unavailable');
    assert(cookies.has('rb_csrf'), label + ' login CSRF cookie missing');
    assert.equal((await c.api('/api/login', 'POST', {username: expected.email, password: process.env.RB_E2E_USER_PASSWORD || 'RB-e2e-permanent-2026!'})).status, 303, label + ' login failed');
    c.loggedIn = true;
    const account = await c.api('/api/account');
    assert.equal(account.status, 200, label + ' account unavailable');
    assert.equal(account.data?.id, expected.owner, label + ' owner mismatch');
    assert.equal(account.data?.role, 'user', label + ' must be an ordinary user');
    assert.equal(account.data?.status, 'active', label + ' must be active');
    assert.equal(account.data?.mustChangePassword, false, label + ' must have completed first password change');
  };
  c.logout = async () => {
    const response = await c.api('/api/logout', 'POST', {});
    assert.equal(response.status, 303, label + ' logout failed');
    c.loggedIn = false;
  };
  clients.push(c);
  return c;
}

function connect(base, auth, kind, path) {
  const observation = {login: auth.label, kind, path, handshakeAccepted: false, payloadBytes: 0, binaryMessages: 0, pongs: 0};
  report.transports.push(observation);
  return new Promise((resolve, reject) => {
    const key = randomBytes(16).toString('base64');
    const signature = createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
    const headers = {Cookie: auth.cookie(), Origin: base.origin, Connection: 'Upgrade', Upgrade: 'websocket', 'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': key};
    if (kind === 'novnc') headers['Sec-WebSocket-Protocol'] = 'binary';
    const req = (base.protocol === 'https:' ? httpsRequest : httpRequest)(new URL(path, base), {method: 'GET', headers});
    let settled = false, socket, buffer = Buffer.alloc(0), cleanup = false;
    const channel = {observation, isOpen: () => !!socket && !socket.destroyed && !observation.closedAt && !observation.serverCloseCode,
      ping: () => socket.write(controlFrame(0x9, Buffer.from('revocation-probe'))),
      cleanup: () => { cleanup = true; socket?.destroy(); req.destroy(); },
    };
    const timer = setTimeout(() => fail('WebSocket handshake timed out'), 10000);
    function fail(message) {
      observation.error = message;
      if (!settled) { settled = true; clearTimeout(timer); socket?.destroy(); req.destroy(); reject(Error(auth.label + '/' + kind + ': ' + message)); }
      else if (!cleanup) socket?.destroy();
    }
    function data(chunk) {
      buffer = buffer.length ? Buffer.concat([buffer, chunk]) : chunk;
      try {
        while (buffer.length >= 2) {
          const opcode = buffer[0] & 0x0f;
          assert.equal(buffer[0] & 0x70, 0, 'reserved WebSocket bits');
          assert.equal(buffer[1] & 0x80, 0, 'masked server frame');
          let length = buffer[1] & 0x7f, offset = 2;
          if (length === 126) { if (buffer.length < 4) return; length = buffer.readUInt16BE(2); offset = 4; }
          if (length === 127) { if (buffer.length < 10) return; const big = buffer.readBigUInt64BE(2); assert(big <= 4n * 1024n * 1024n, 'oversized server frame'); length = Number(big); offset = 10; }
          assert(length <= 4 * 1024 * 1024, 'oversized server frame');
          if (buffer.length < offset + length) return;
          const payload = buffer.subarray(offset, offset + length);
          buffer = buffer.subarray(offset + length);
          if (opcode === 0x2 || opcode === 0x0) {
            observation.payloadBytes += length;
            observation.binaryMessages++;
            if (kind === 'novnc' && /^RFB \d{3}\.\d{3}\n$/.test(payload.toString('ascii'))) observation.rfbGreetingReceived = true;
          } else if (opcode === 0x9) socket.write(controlFrame(0xa, payload));
          else if (opcode === 0xa) observation.pongs++;
          else if (opcode === 0x8) {
            observation.serverCloseCode = payload.length >= 2 ? payload.readUInt16BE(0) : 1005;
            socket.end(controlFrame(0x8, payload));
          } else if (opcode !== 0x1) throw Error('unknown WebSocket opcode');
        }
      } catch { fail('invalid WebSocket frame'); }
    }
    req.on('upgrade', (response, transport, head) => {
      socket = transport;
      sockets.push(channel);
      socket.on('error', error => { observation.transportErrorCode = error.code || error.name; });
      socket.on('close', () => {
        if (cleanup) return;
        observation.closedAt = new Date().toISOString();
        observation.closedMonotonic = performance.now();
        if (logoutStarted !== undefined) observation.afterLogoutMs = Math.round(observation.closedMonotonic - logoutStarted);
      });
      socket.on('data', data);
      try {
        assert.equal(response.statusCode, 101);
        assert.equal(response.headers['sec-websocket-accept'], signature);
        assert.equal(String(response.headers.upgrade).toLowerCase(), 'websocket');
        if (kind === 'pcm') {
          const format = String(response.headers['x-audio-format']).replace(/\s/g, '').toLowerCase();
          assert.equal(format, 's16le;rate=48000;channels=2');
          observation.formatHeader = format;
        }
        observation.handshakeAccepted = true;
        observation.connectedAt = new Date().toISOString();
        if (head.length) data(head);
        settled = true; clearTimeout(timer); resolve(channel);
      } catch { fail('invalid WebSocket upgrade'); }
    });
    req.on('response', response => { observation.rejectedHTTPStatus = response.statusCode; response.resume(); fail('WebSocket rejected HTTP ' + response.statusCode); });
    req.on('error', () => { if (!settled) fail('WebSocket connection failed'); });
    req.end();
  });
}

async function waitUntil(test, maxMilliseconds, error) {
  const started = performance.now();
  while (!test()) {
    assert(performance.now() - started < maxMilliseconds, error);
    await pause(50);
  }
}

try {
  const base = new URL(process.env.RB_E2E_URL || 'http://localhost');
  assert(['http:', 'https:'].includes(base.protocol) && ['localhost', '127.0.0.1'].includes(base.hostname), 'only local test deployment allowed');
  assert(!base.username && !base.password && base.pathname === '/' && !base.search && !base.hash, 'a plain local origin is required');
  const platform = JSON.parse(await readFile('data-e2e/platform-report.json', 'utf8'));
  assert.equal(platform.result, 'passed'); assert.equal(platform.run, expected.run);
  assert(platform.instances.some(s => s.id === expected.id && s.owner === expected.owner), 'authorized Bob fixture missing');
  assert(platform.users.some(u => u.id === expected.owner && u.email === expected.email), 'authorized Bob account missing');
  Object.assign(report, {origin: base.origin, platformRun: expected.run, instanceID: expected.id, ownerID: expected.owner});
  const before = inspect();
  assert.equal(before.State.Running, true); assert.equal(before.Config.Labels['remotebrowser.user.id'], String(expected.owner));
  report.before = {containerID: before.Id, startedAt: before.State.StartedAt, restartCount: before.RestartCount};
  const primary = client(base, 'revoked-login'), control = client(base, 'independent-login');
  phase = 'independent-authentication';
  await primary.login(); await control.login();
  assert(primary.session() && control.session(), 'authenticated session cookies missing');
  assert.notEqual(primary.session(), control.session(), 'independent sessions unexpectedly share an authentication cookie');
  assert.equal((await primary.api('/api/sessions/' + expected.id)).data?.status, 'running', 'Bob instance not running');
  phase = 'establish-six-real-transports';
  const endpoints = [['pcm', '/audio'], ['input', '/input'], ['novnc', '/novnc/websockify']];
  const primaryChannels = [], controlChannels = [];
  for (const [kind, path] of endpoints) {
    primaryChannels.push(await connect(base, primary, kind, '/sessions/' + expected.id + path));
    controlChannels.push(await connect(base, control, kind, '/sessions/' + expected.id + path));
  }
  for (const channel of sockets.filter(c => c.observation.kind !== 'pcm')) channel.ping();
  await waitUntil(() => sockets.every(c => c.observation.kind === 'pcm' ? c.observation.binaryMessages > 0 : c.observation.pongs > 0), 5000, 'initial transport liveness missing');
  assert(sockets.filter(c => c.observation.kind === 'novnc').every(c => c.observation.rfbGreetingReceived), 'noVNC upstream RFB greeting missing');
  await pause(500);
  assert(sockets.every(c => c.isOpen()), 'a transport closed before revocation');
  report.beforeLogout = sockets.map(c => ({login: c.observation.login, kind: c.observation.kind, open: c.isOpen(), payloadBytes: c.observation.payloadBytes, pongs: c.observation.pongs}));
  const originalCookie = primary.cookie();
  phase = 'logout-one-authentication-session';
  logoutStarted = performance.now(); report.logoutRequestedAt = new Date().toISOString();
  await primary.logout();
  report.logoutHTTPCompletedMs = Math.round(performance.now() - logoutStarted);
  await waitUntil(() => primaryChannels.every(c => c.observation.closedAt), 10000 - (performance.now() - logoutStarted), 'revoked transports survived the ten-second limit');
  assert(primaryChannels.every(c => c.observation.afterLogoutMs >= 0 && c.observation.afterLogoutMs <= 10000), 'transport closed outside revocation window');
  report.maximumRevocationMs = Math.max(...primaryChannels.map(c => c.observation.afterLogoutMs));
  assert(primaryChannels.every(c => !c.observation.error), 'a client-side protocol failure closed a revoked transport');
  phase = 'verify-independent-session-and-container';
  assert(controlChannels.every(c => c.isOpen()), 'independent session transport incorrectly revoked');
  const prior = controlChannels.map(c => ({bytes: c.observation.payloadBytes, pongs: c.observation.pongs}));
  for (const channel of controlChannels.filter(c => c.observation.kind !== 'pcm')) channel.ping();
  await waitUntil(() => controlChannels.every((c, i) => c.observation.kind === 'pcm' ? c.observation.payloadBytes > prior[i].bytes : c.observation.pongs > prior[i].pongs), 3000, 'independent transport stopped responding after other login logout');
  const account = await control.api('/api/account'); assert.equal(account.status, 200); assert.equal(account.data?.id, expected.owner);
  const revoked = await fetch(new URL('/api/account', base), {headers: {Accept: 'application/json', Cookie: originalCookie}, redirect: 'manual', signal: AbortSignal.timeout(10000)});
  assert.equal(revoked.status, 401, 'the original login cookie remained valid'); await revoked.arrayBuffer();
  const after = inspect();
  assert.equal(after.Id, before.Id, 'Bob container replaced'); assert.equal(after.State.Running, true, 'Bob container stopped');
  assert.equal(after.State.StartedAt, before.State.StartedAt, 'Bob container restarted'); assert.equal(after.RestartCount, before.RestartCount, 'Bob restart count changed');
  assert(sockets.every(c => !c.observation.error), 'client-side WebSocket protocol error detected');
  report.after = {containerID: after.Id, startedAt: after.State.StartedAt, restartCount: after.RestartCount, running: true, originalCookieHTTPStatus: revoked.status, independentAccountHTTPStatus: account.status, independentTransportsStillOpen: controlChannels.every(c => c.isOpen())};
  report.result = 'passed';
} catch (error) {
  report.error = error.message; report.failedPhase = phase; process.exitCode = 1;
} finally {
  // Record the observed result before closing only this script's sockets.
  // Logging out the control session is test cleanup, not revocation evidence.
  for (const channel of sockets) channel.cleanup();
  for (const c of clients.filter(c => c.loggedIn)) {
    try { await c.logout(); } catch { report.cleanupError = 'test login logout failed'; report.result = 'failed'; process.exitCode = 1; }
  }
  for (const transport of report.transports) delete transport.closedMonotonic;
  report.completedAt = new Date().toISOString();
  await mkdir('data-e2e', {recursive: true});
  await writeFile('data-e2e/transport-revocation-report.json', JSON.stringify(report, null, 2) + '\n', {mode: 0o600});
  console.log(JSON.stringify({result: report.result, maximumRevocationMs: report.maximumRevocationMs, independentTransportsStillOpen: report.after?.independentTransportsStillOpen, error: report.error, failedPhase: report.failedPhase}));
}
