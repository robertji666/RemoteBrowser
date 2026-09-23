// Authenticated PCM transport check against an existing disposable E2E user.
// Usage: node scripts/e2e-audio.mjs A [--seconds=30]
//        node scripts/e2e-audio.mjs B
//        node scripts/e2e-audio.mjs sess_<id>
// Start this check, then play the browser-acceptance fixture through the UI.
// It does not start playback, touch Docker, install packages or save raw audio.
// PASS means measurable PCM reached an authenticated WebSocket, not that audio
// sounded correct, WebRTC worked, or the end user's speakers were audible.

import { createHash, randomBytes } from 'node:crypto';
import { request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { mkdir, readFile, writeFile } from 'node:fs/promises';

const RATE = 48000;
const CHANNELS = 2;
const BYTES_PER_SAMPLE = 2;
const THRESHOLDS = {
  minimumCapturedSeconds: 1,
  minimumPeakInt16: 256,
  minimumRmsInt16: 32,
  minimumSignificantSamples: 2400,
  minimumSignificantFraction: 0.01,
};
const state = {
  startedAt: new Date().toISOString(),
  result: 'failed',
  scope: 'Authenticated PCM WebSocket transport only; no listening or WebRTC assertion',
  expectedFormat: { encoding: 's16le', sampleRate: RATE, channels: CHANNELS, interleaved: true },
  thresholds: THRESHOLDS,
  handshakeAccepted: false,
  bytes: 0,
  binaryMessages: 0,
  samples: 0,
  nonzeroSamples: 0,
  significantSamples: 0,
  peakInt16: 0,
};
let sumSquares = 0;
const channels = Array.from({length: CHANNELS}, () => ({samples: 0, peak: 0, sumSquares: 0}));
let phase = 'select-test-instance';
let outputPath;

function check(condition, message) {
  if (!condition) throw Error(message);
}

function updatePCM(data) {
  check(data.length % (CHANNELS * BYTES_PER_SAMPLE) === 0, 'PCM binary message is not stereo int16 aligned');
  state.bytes += data.length;
  state.binaryMessages++;
  for (let offset = 0; offset < data.length; offset += BYTES_PER_SAMPLE) {
    const value = data.readInt16LE(offset);
    const magnitude = Math.abs(value);
    const channel = channels[(offset / BYTES_PER_SAMPLE) % CHANNELS];
    state.samples++;
    if (value !== 0) state.nonzeroSamples++;
    if (magnitude >= THRESHOLDS.minimumPeakInt16) state.significantSamples++;
    state.peakInt16 = Math.max(state.peakInt16, magnitude);
    sumSquares += value * value;
    channel.samples++;
    channel.peak = Math.max(channel.peak, magnitude);
    channel.sumSquares += value * value;
  }
}

function statistics() {
  const rmsInt16 = state.samples ? Math.sqrt(sumSquares / state.samples) : 0;
  return {
    capturedPCMSeconds: state.bytes / (RATE * CHANNELS * BYTES_PER_SAMPLE),
    rmsInt16,
    rmsNormalized: rmsInt16 / 32768,
    peakNormalized: state.peakInt16 / 32768,
    significantFraction: state.samples ? state.significantSamples / state.samples : 0,
    channels: channels.map((channel, index) => ({
      channel: index,
      samples: channel.samples,
      peakInt16: channel.peak,
      rmsInt16: channel.samples ? Math.sqrt(channel.sumSquares / channel.samples) : 0,
    })),
  };
}

function audibleDataPresent() {
  const stats = statistics();
  return stats.capturedPCMSeconds >= THRESHOLDS.minimumCapturedSeconds &&
    state.peakInt16 >= THRESHOLDS.minimumPeakInt16 &&
    stats.rmsInt16 >= THRESHOLDS.minimumRmsInt16 &&
    state.significantSamples >= THRESHOLDS.minimumSignificantSamples &&
    stats.significantFraction >= THRESHOLDS.minimumSignificantFraction;
}

// Client-to-server control frames must be masked. No audio/data is transmitted
// by this script; this helper only responds to ping and closes the connection.
function controlFrame(opcode, payload = Buffer.alloc(0)) {
  check(payload.length <= 125, 'WebSocket control payload too large');
  const mask = randomBytes(4);
  const frame = Buffer.alloc(6 + payload.length);
  frame[0] = 0x80 | opcode;
  frame[1] = 0x80 | payload.length;
  mask.copy(frame, 2);
  for (let i = 0; i < payload.length; i++) frame[6 + i] = payload[i] ^ mask[i % 4];
  return frame;
}

function capturePCM(origin, instanceID, cookieHeader, seconds) {
  return new Promise((resolve, reject) => {
    const key = randomBytes(16).toString('base64');
    const accept = createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
    const target = new URL('/sessions/' + instanceID + '/audio', origin);
    const request = target.protocol === 'https:' ? httpsRequest : httpRequest;
    let socket;
    let buffer = Buffer.alloc(0);
    let captureTimer;
    let finished = false;
    const connectedAt = { value: 0 };
    const connectTimer = setTimeout(() => finish(Error('WebSocket handshake timed out')), 10000);
    const req = request(target, {
      method: 'GET',
      headers: {
        Cookie: cookieHeader,
        Origin: origin,
        Connection: 'Upgrade',
        Upgrade: 'websocket',
        'Sec-WebSocket-Version': '13',
        'Sec-WebSocket-Key': key,
      },
    });

    function finish(error) {
      if (finished) return;
      finished = true;
      clearTimeout(connectTimer);
      clearTimeout(captureTimer);
      if (connectedAt.value) state.captureWallMilliseconds = Date.now() - connectedAt.value;
      if (socket && !socket.destroyed) {
        socket.end(controlFrame(0x8, Buffer.from([0x03, 0xe8])));
        // The Python sender does not consume close frames. Bound its lifetime
        // on this side instead of waiting indefinitely for a close handshake.
        const closeTimer = setTimeout(() => socket.destroy(), 250);
        closeTimer.unref();
      } else req.destroy();
      if (error) reject(error); else resolve();
    }

    function frames(chunk) {
      if (finished) return;
      buffer = buffer.length ? Buffer.concat([buffer, chunk]) : chunk;
      try {
        while (buffer.length >= 2 && !finished) {
          const first = buffer[0], second = buffer[1];
          const opcode = first & 0x0f;
          check((first & 0x70) === 0, 'Unexpected compressed/reserved WebSocket frame');
          check((second & 0x80) === 0, 'Server WebSocket frame must not be masked');
          check((first & 0x80) !== 0, 'Audio sender unexpectedly fragmented a WebSocket message');
          let offset = 2;
          let length = second & 0x7f;
          if (length === 126) {
            if (buffer.length < 4) return;
            length = buffer.readUInt16BE(2); offset = 4;
          } else if (length === 127) {
            if (buffer.length < 10) return;
            const bigLength = buffer.readBigUInt64BE(2);
            check(bigLength <= 1024n * 1024n, 'Audio WebSocket frame exceeds 1 MiB');
            length = Number(bigLength); offset = 10;
          }
          check(length <= 1024 * 1024, 'Audio WebSocket frame exceeds 1 MiB');
          if (buffer.length < offset + length) return;
          const payload = buffer.subarray(offset, offset + length);
          buffer = buffer.subarray(offset + length);
          if (opcode === 0x2) {
            updatePCM(payload);
            if (audibleDataPresent()) finish();
          } else if (opcode === 0x9) {
            check(payload.length <= 125, 'Invalid WebSocket ping size');
            socket.write(controlFrame(0xa, payload));
          } else if (opcode === 0x8) {
            state.serverCloseCode = payload.length >= 2 ? payload.readUInt16BE(0) : null;
            finish(Error('Audio WebSocket closed before sufficient nonzero PCM arrived'));
          } else if (opcode !== 0xa) {
            throw Error('Audio WebSocket received a non-PCM message');
          }
        }
      } catch (error) { finish(error); }
    }

    req.on('upgrade', (response, upgradedSocket, head) => {
      socket = upgradedSocket;
      // Install transport listeners before validating the upgrade so a bad
      // response cannot leave an unhandled socket error during shutdown.
      socket.on('error', () => finish(Error('Audio WebSocket transport error')));
      socket.on('end', () => finish(Error('Audio WebSocket ended before sufficient nonzero PCM arrived')));
      socket.on('close', () => finish(Error('Audio WebSocket closed before sufficient nonzero PCM arrived')));
      clearTimeout(connectTimer);
      try {
        check(response.statusCode === 101, 'Audio WebSocket upgrade was not accepted');
        check(response.headers['sec-websocket-accept'] === accept, 'Invalid WebSocket accept signature');
        check(String(response.headers.upgrade || '').toLowerCase() === 'websocket', 'Invalid WebSocket upgrade header');
        const format = String(response.headers['x-audio-format'] || '').replace(/\s/g, '').toLowerCase();
        check(format === 's16le;rate=48000;channels=2', 'Missing or unsupported X-Audio-Format header');
        state.formatHeader = format;
        state.handshakeAccepted = true;
        state.captureStartedAt = new Date().toISOString();
        connectedAt.value = Date.now();
        console.log('PCM capture connected; play the 440 Hz browser fixture now (maximum ' + seconds + ' seconds).');
        captureTimer = setTimeout(() => finish(Error(state.bytes ? 'Capture timed out with silence or insufficient nonzero PCM' : 'Capture timed out without binary PCM')), seconds * 1000);
        socket.on('data', frames);
        if (head.length) frames(head);
      } catch (error) { finish(error); }
    });
    req.on('response', response => {
      state.handshakeHTTPStatus = response.statusCode;
      response.resume();
      finish(Error('Audio endpoint rejected WebSocket upgrade (HTTP ' + response.statusCode + ')'));
    });
    req.on('error', () => finish(Error('Audio connection failed')));
    req.end();
  });
}

try {
  const args = process.argv.slice(2);
  const selector = args.find(arg => !arg.startsWith('--')) || 'A';
  const unknown = args.filter(arg => arg.startsWith('--') && !arg.startsWith('--seconds='));
  check(unknown.length === 0, 'Usage: e2e-audio.mjs A|B|sess_<id> [--seconds=1..30]');
  const seconds = Number(args.find(arg => arg.startsWith('--seconds='))?.slice('--seconds='.length) || 30);
  check(Number.isFinite(seconds) && seconds >= 1 && seconds <= 30, 'Capture duration must be 1 to 30 seconds');
  const base = new URL(process.env.RB_E2E_URL || 'http://localhost');
  check(['http:', 'https:'].includes(base.protocol) && ['localhost', '127.0.0.1'].includes(base.hostname), 'Only a local test deployment is allowed');
  check(!base.username && !base.password && base.pathname === '/' && !base.search && !base.hash, 'Use a plain local origin without credentials or a path');
  const platform = JSON.parse(await readFile('data-e2e/platform-report.json', 'utf8'));
  check(platform.result === 'passed', 'A passed platform report is required');
  const index = {A: 0, B: 1}[selector.toUpperCase()];
  const instance = index === undefined ? platform.instances?.find(item => item.id === selector) : platform.instances?.[index];
  check(instance && /^sess_[a-zA-Z0-9_-]+$/.test(instance.id), 'Requested test instance was not found in the platform report');
  const owner = platform.users?.find(user => user.id === instance.owner);
  check(owner && /^e2e-[^@]+@example\.test$/.test(owner.email), 'Only an existing platform-test ordinary user may be used');
  outputPath = 'data-e2e/audio-report-' + instance.id + '.json';
  Object.assign(state, {platformRun: platform.run, instanceID: instance.id, ownerID: owner.id, origin: base.origin, maximumCaptureSeconds: seconds});

  const cookies = new Map();
  const cookieHeader = () => [...cookies].map(([name, value]) => name + '=' + value).join('; ');
  async function api(path, options = {}) {
    const headers = {Accept: 'application/json', ...options.headers};
    if (cookies.size) headers.Cookie = cookieHeader();
    const response = await fetch(new URL(path, base), {...options, headers, redirect: 'manual', signal: AbortSignal.timeout(10000)});
    for (const raw of response.headers.getSetCookie()) {
      const pair = raw.split(';', 1)[0], equals = pair.indexOf('=');
      if (equals > 0) cookies.set(pair.slice(0, equals), pair.slice(equals + 1));
    }
    return response;
  }
  phase = 'authenticate-test-user';
  const page = await api('/login');
  check(page.status === 200, 'Could not load login form');
  await page.arrayBuffer();
  check(cookies.has('rb_csrf'), 'Login form did not issue a CSRF cookie');
  const login = await api('/api/login', {
    method: 'POST',
    headers: {'X-CSRF-Token': cookies.get('rb_csrf'), Origin: base.origin},
    body: new URLSearchParams({username: owner.email, password: process.env.RB_E2E_USER_PASSWORD || 'RB-e2e-permanent-2026!'}),
  });
  check(login.status === 303 && cookies.has('rb_session'), 'Test-user login failed; credentials are never included in this report');
  await login.arrayBuffer();
  const accountResponse = await api('/api/account');
  check(accountResponse.status === 200, 'Authenticated account is not available');
  const account = await accountResponse.json();
  check(account.id === owner.id && account.role === 'user' && account.status === 'active' && !account.mustChangePassword, 'Selected account is not the expected active ordinary test user');
  const instanceResponse = await api('/api/sessions/' + instance.id);
  check(instanceResponse.status === 200, 'Selected instance is not accessible to its reported owner');
  const current = await instanceResponse.json();
  check(current.status === 'running', 'Selected test instance is not running');

  phase = 'capture-authenticated-pcm';
  await capturePCM(base.origin, instance.id, cookieHeader(), seconds);
  check(audibleDataPresent(), 'Capture did not contain sufficient nonzero PCM');
  state.result = 'passed';
} catch (error) {
  state.result = 'failed';
  // Every error raised by this script is a fixed diagnostic, not response body,
  // request headers or credentials. Native errors are reduced to their name.
  state.error = error?.name === 'Error' ? error.message : (error?.name || 'UnknownError');
  state.failedPhase = phase;
  process.exitCode = 1;
} finally {
  Object.assign(state, statistics(), {completedAt: new Date().toISOString()});
  outputPath ||= 'data-e2e/audio-report-selection-error.json';
  await mkdir('data-e2e', {recursive: true});
  await writeFile(outputPath, JSON.stringify(state, null, 2) + '\n', {mode: 0o600});
  console.log(state.result.toUpperCase() + ' authenticated PCM transport: bytes=' + state.bytes + ', peak=' + state.peakInt16 + ', RMS=' + state.rmsInt16.toFixed(2));
  if (state.error) console.error(state.error);
  console.log('Report: ' + outputPath);
}
