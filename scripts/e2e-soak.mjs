// Usage (repository root): node scripts/e2e-soak.mjs start|check sess_<id>
// The ID must belong to an A/B user in a successful platform-report.json.
// Reads container/SQLite/profile state; account login and metadata GET do not
// open remote-control connections or update an instance's last_active_at.
// This script never changes database rows, timestamps, instances or bookmarks.
// A passed 24-hour infrastructure check still requires a real browser reconnect.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {existsSync} from 'node:fs';
import {mkdir, readFile, rename, stat, writeFile} from 'node:fs/promises';
import {basename, dirname, isAbsolute, join, resolve} from 'node:path';

const [command, instanceID, ...extra] = process.argv.slice(2);
assert(['start', 'check'].includes(command) && /^sess_[a-f0-9]+$/.test(instanceID || '') && extra.length === 0,
  'Usage: node scripts/e2e-soak.mjs start|check sess_<id>');
const MINIMUM_ELAPSED_MS = 24 * 60 * 60 * 1000;
const reportPath = 'data-e2e/soak-report.json';
const origin = process.env.RB_E2E_URL || 'http://localhost';
const target = new URL(origin);
assert(['localhost', '127.0.0.1'].includes(target.hostname) && !target.username && !target.password,
  'only an uncredentialed local HTTP origin is permitted');
const docker = (...args) => execFileSync('docker', args, {
  encoding: 'utf8', timeout: 30000, stdio: ['ignore', 'pipe', 'pipe'],
}).trim();
const inspect = name => JSON.parse(docker('inspect', name))[0];
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const digest = content => createHash('sha256').update(content).digest('hex');
const readValue = (source, key) => {
  const raw = source.split(/\r?\n/).find(line => line.startsWith(key + '='))?.slice(key.length + 1).trim();
  if (!raw) return '';
  if ((raw.startsWith('"') && raw.endsWith('"')) || (raw.startsWith("'") && raw.endsWith("'"))) return raw.slice(1, -1);
  return raw.replace(/\s+#.*$/, '');
};

class MetadataClient {
  cookies = new Map();
  csrf = '';
  async request(path, method = 'GET', form) {
    const headers = {Accept: 'application/json'};
    if (this.cookies.size) headers.Cookie = [...this.cookies].map(([k, v]) => k + '=' + v).join('; ');
    if (method !== 'GET') headers['X-CSRF-Token'] = this.csrf;
    const response = await fetch(origin + path, {method, headers,
      body: form ? new URLSearchParams(form) : undefined, redirect: 'manual', signal: AbortSignal.timeout(15000)});
    for (const cookie of response.headers.getSetCookie()) {
      const first = cookie.split(';')[0]; const i = first.indexOf('=');
      this.cookies.set(first.slice(0, i), first.slice(i + 1));
    }
    const text = await response.text();
    let data; try { data = JSON.parse(text); } catch {}
    return {status: response.status, data};
  }
  async readMetadata(user) {
    const password = process.env.RB_E2E_SOAK_PASSWORD || 'RB-e2e-permanent-2026!';
    await this.request('/login');
    this.csrf = this.cookies.get('rb_csrf');
    assert.equal((await this.request('/api/login', 'POST', {username: user.email, password})).status, 303,
      'test owner login failed; supply RB_E2E_SOAK_PASSWORD if its test password changed');
    try {
      const response = await this.request('/api/sessions/' + instanceID);
      assert.equal(response.status, 200, 'owner metadata read failed');
      return response.data;
    } finally {
      assert.equal((await this.request('/api/logout', 'POST', {})).status, 303, 'test observer login could not be revoked');
    }
  }
}

function flattenBookmarks(node, path = []) {
  if (!node || typeof node !== 'object') return [];
  if (node.type === 'url') return [{id: node.id || '', name: node.name || '', url: node.url, path}];
  return (node.children || []).flatMap(child => flattenBookmarks(child, [...path, node.name || '']));
}

// Read-only /proc observations supplement the server's counter. In particular,
// older counters did not cover standalone audio streams or WebRTC media peers.
const procProbe = String.raw`
import json
from pathlib import Path
ports = {6080, 6081, 6084}
sockets = []
for name in ('tcp', 'tcp6'):
    for line in Path('/proc/net/' + name).read_text().splitlines()[1:]:
        fields = line.split()
        port = int(fields[1].rsplit(':', 1)[1], 16)
        if port in ports and fields[3] == '01':
            sockets.append({'protocol': name, 'port': port, 'state': 'ESTABLISHED'})
print(json.dumps({'kernelBootID': Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
                  'kernelUptimeSeconds': float(Path('/proc/uptime').read_text().split()[0]),
                  'desktopEstablishedSockets': sockets}))
`;

let configuration;
async function sample() {
  const {user, hostDataDir, platform} = configuration;
  const name = 'rb-sess-' + instanceID;
  const container = inspect(name);
  assert.equal(container.Config.Labels['remotebrowser.session.id'], instanceID, 'container instance ownership changed');
  assert.equal(container.Config.Labels['remotebrowser.user.id'], String(user.id), 'container user ownership changed');
  assert.equal(container.State.Running, true, 'soak instance is not running');
  assert.equal(container.State.Paused, false, 'soak instance is paused');
  assert.equal(container.State.Health?.Status, 'healthy', 'soak container is not healthy');
  const profile = container.Mounts.find(m => m.Type === 'bind' && m.Destination === '/home/rbuser/profile')?.Source;
  const downloads = container.Mounts.find(m => m.Type === 'bind' && ['/home/rbuser/Downloads', '/home/rbuser/downloads'].includes(m.Destination))?.Source;
  assert.equal(profile, join(hostDataDir, 'sessions', instanceID, 'profile'), 'profile path does not match the explicit instance');
  assert.equal(downloads, join(hostDataDir, 'sessions', instanceID, 'downloads'), 'download path does not match the explicit instance');
  const profileInfo = await stat(profile);
  assert(profileInfo.isDirectory() && existsSync(join(profile, 'Default', 'Preferences')), 'persisted Chromium profile is missing');
  const bookmarkFile = join(profile, 'Default', 'Bookmarks');
  const bookmarksDocument = JSON.parse(await readFile(bookmarkFile, 'utf8'));
  const bookmarks = Object.values(bookmarksDocument.roots || {}).flatMap(root => flattenBookmarks(root))
    .sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
  assert(bookmarks.length > 0, 'prepare a real browser bookmark before starting the soak');
  const expectedBookmark = process.env.RB_E2E_SOAK_BOOKMARK_URL;
  if (expectedBookmark) assert(bookmarks.some(bookmark => bookmark.url === expectedBookmark), 'expected browser bookmark is absent');

  const platformFilename = user.label === 'bob' ? '隔离验收.txt' : '持久化验收.txt';
  const platformExpected = user.label === 'bob' ? 'Only user B ' + platform.run : 'RemoteBrowser 中文持久化 ' + platform.run;
  assert.equal(await readFile(join(downloads, platformFilename), 'utf8'), platformExpected, 'platform download fixture differs');
  const filenames = new Set([platformFilename]);
  const browserFilename = process.env.RB_E2E_SOAK_DOWNLOAD || '浏览器下载验收.txt';
  assert.equal(basename(browserFilename), browserFilename, 'download marker must be a plain filename');
  if (process.env.RB_E2E_SOAK_DOWNLOAD || existsSync(join(downloads, browserFilename))) filenames.add(browserFilename);
  const downloadMarkers = [];
  for (const filename of [...filenames].sort()) {
    const content = await readFile(join(downloads, filename));
    downloadMarkers.push({filename, sha256: digest(content), bytes: content.length});
  }

  // -readonly forbids writes and creation. The only SQL is SELECT; no timestamp
  // adjustments, PRAGMAs changing storage, or synthetic expiry manipulation.
  const rows = JSON.parse(execFileSync('sqlite3', ['-readonly', '-json', '-cmd', '.timeout 5000',
    join(hostDataDir, 'manager.db'),
    `SELECT id,user_id,status,desired_state,last_active_at,expired_at FROM sessions WHERE id='${instanceID}'`],
  {encoding: 'utf8', timeout: 15000, stdio: ['ignore', 'pipe', 'pipe']}));
  assert.equal(rows.length, 1, 'instance database row missing');
  const database = rows[0];
  assert.equal(database.user_id, user.id, 'database owner differs from platform report');
  assert.equal(database.status, 'running', 'database does not show running');
  assert.equal(database.desired_state, 'running', 'instance is not intended to keep running');
  assert.equal(database.expired_at, null, 'instance has an expiry record');
  assert(database.last_active_at, 'last-active evidence required after browser preparation');

  const metadata = await new MetadataClient().readMetadata(user);
  assert.equal(metadata.id, instanceID, 'metadata returned a different instance');
  assert.equal(metadata.status, 'running', 'API does not show running');
  assert.equal(metadata.connectedCount, 0, 'backend reports an active remote connection; close all instance tabs first');
  // /stats is an internal, read-only endpoint, not the public remote proxy.
  // Missing endpoint or absent peerCount is a hard failure, never assumed zero.
  const media = JSON.parse(docker('exec', name, 'curl', '--fail', '--silent', '--show-error', '--max-time', '5', 'http://127.0.0.1:6082/stats'));
  assert.equal(media.peerCount, 0, 'WebRTC media peers remain connected');
  const proc = JSON.parse(docker('exec', name, 'python3', '-c', procProbe));
  assert.equal(proc.desktopEstablishedSockets.length, 0, 'desktop/input/audio TCP connections remain open');
  assert(Number.isFinite(proc.kernelUptimeSeconds), 'kernel uptime observation missing');

  // A manager replacement would reset in-memory counters. Track the manager
  // managing this exact data mount to prevent such an evidence gap going silent.
  const managerIDs = docker('ps', '--filter', 'label=com.docker.compose.service=manager', '--format', '{{.ID}}').split('\n').filter(Boolean);
  const managers = managerIDs.map(inspect).filter(c => c.Mounts.some(m => m.Type === 'bind' && resolve(m.Source) === hostDataDir && m.Destination === '/data'));
  assert.equal(managers.length, 1, 'exactly one running Compose manager for this data directory is required');
  const manager = managers[0];
  return {at: new Date().toISOString(), epochMs: Date.now(), instanceID, ownerID: user.id,
    containerID: container.Id, containerName: name, imageID: container.Image, imageReference: container.Config.Image,
    startedAt: container.State.StartedAt, restartCount: container.RestartCount, running: true, healthy: true,
    manager: {containerID: manager.Id, startedAt: manager.State.StartedAt},
    profile: {path: profile, dev: profileInfo.dev, ino: profileInfo.ino, bookmarkFile, bookmarks},
    downloads: {path: downloads, markers: downloadMarkers}, database,
    observations: {backendConnectedCount: metadata.connectedCount, apiLastActiveAt: metadata.lastActiveAt,
      webRTCPeerCount: media.peerCount, ...proc}};
}

function assertUnchanged(before, after) {
  for (const key of ['instanceID', 'ownerID', 'containerID', 'imageID', 'imageReference', 'startedAt', 'restartCount']) {
    assert.deepEqual(after[key], before[key], 'continuity changed: ' + key);
  }
  assert.deepEqual(after.manager, before.manager, 'manager restarted; continuous access-counter evidence is no longer available');
  assert.deepEqual(after.profile, before.profile, 'profile identity or bookmark markers changed');
  assert.deepEqual(after.downloads, before.downloads, 'download markers changed');
  assert.deepEqual(after.database, before.database, 'database status, ownership or last_active_at changed during unattended period');
  assert.equal(after.observations.kernelBootID, before.observations.kernelBootID, 'Docker kernel restarted');
  assert.equal(after.observations.apiLastActiveAt, before.observations.apiLastActiveAt, 'API last activity changed');
}

let report;
let maySave = false;
try {
  if (command === 'start') {
    assert(!existsSync(reportPath), 'soak report already exists; do not silently reset its start time. Archive it explicitly to begin a new run');
    report = {version: 1, instanceID, origin, result: 'pending', minimumElapsedMs: MINIMUM_ELAPSED_MS,
      browserReconnect: 'not_run_required_after_24h', samples: [], limitations: [
        'Only sampled observations and unchanged access timestamps are evidence; no synthetic time advance is used.',
        'A metadata login and GET do not create a remote-control connection. No /sessions/<id>/ proxy routes are requested.',
        'A real CUA browser reconnect and visual data check is still required after the 24-hour unattended check passes.',
      ]};
    maySave = true;
  } else {
    report = JSON.parse(await readFile(reportPath, 'utf8'));
    assert.equal(report.version, 1, 'unsupported soak report');
    assert.equal(report.instanceID, instanceID, 'explicit ID differs from saved soak target');
    assert.equal(report.origin, origin, 'soak target origin changed');
    assert(report.baseline, 'no valid unattended baseline was established');
    assert.notEqual(report.result, 'failed', 'soak was already invalidated; inspect evidence before explicitly starting a new run');
    maySave = true;
  }
  const platform = JSON.parse(await readFile('data-e2e/platform-report.json', 'utf8'));
  assert.equal(platform.result, 'passed', 'successful platform report required');
  const instance = platform.instances.find(s => s.id === instanceID);
  assert(instance, 'explicit instance ID is absent from platform-report.json');
  const user = platform.users.find(u => u.id === instance.owner);
  assert(user && ['alice', 'bob'].includes(user.label) && /^e2e-(alice|bob)-.+@example\.test$/.test(user.email), 'target must belong to a platform A/B test user');
  const env = await readFile('.env', 'utf8');
  const rawDataDir = process.env.RB_SESSION_HOST_DATA_DIR || readValue(env, 'RB_SESSION_HOST_DATA_DIR');
  assert(rawDataDir && isAbsolute(rawDataDir), 'absolute host data directory is required');
  configuration = {platform, user, hostDataDir: resolve(rawDataDir)};
  if (command === 'start') {
    report.platformRun = platform.run;
    report.owner = {id: user.id, email: user.email, label: user.label};
    const first = await sample();
    report.samples.push(first);
    // A second quiet observation prevents treating delayed disconnects or
    // last-active writes as an established unattended baseline.
    await pause(5000);
    const second = await sample();
    report.samples.push(second);
    assertUnchanged(first, second);
    report.baseline = second;
    report.startedAt = second.at;
    report.earliestWallClockCompletion = new Date(second.epochMs + MINIMUM_ELAPSED_MS).toISOString();
    report.elapsedWallMs = 0;
    report.elapsedKernelMs = 0;
    console.log('PENDING unattended baseline verified for ' + instanceID + '; 24 real hours have not elapsed.');
  } else {
    assert.equal(report.platformRun, platform.run, 'platform report changed during soak');
    const current = await sample();
    report.samples.push(current);
    assertUnchanged(report.baseline, current);
    report.elapsedWallMs = current.epochMs - report.baseline.epochMs;
    report.elapsedKernelMs = Math.round((current.observations.kernelUptimeSeconds - report.baseline.observations.kernelUptimeSeconds) * 1000);
    assert(report.elapsedWallMs >= 0 && report.elapsedKernelMs >= 0, 'clock continuity regressed');
    if (report.elapsedWallMs >= MINIMUM_ELAPSED_MS && report.elapsedKernelMs >= MINIMUM_ELAPSED_MS) {
      report.result = 'passed';
      report.completedAt = current.at;
      console.log('PASS actual 24-hour unattended infrastructure retention. Browser reconnect remains NOT VERIFIED.');
    } else {
      report.result = 'pending';
      console.log('PENDING actual elapsed: wall ' + (report.elapsedWallMs / 3600000).toFixed(3) + 'h, Docker kernel ' + (report.elapsedKernelMs / 3600000).toFixed(3) + 'h; both must reach 24h.');
    }
  }
} catch (error) {
  process.exitCode = 1;
  console.error('FAIL ' + error.message);
  if (maySave && report) {
    report.result = 'failed';
    report.failures ||= [];
    report.failures.push({at: new Date().toISOString(), message: error.message});
  }
} finally {
  if (maySave && report) {
    report.updatedAt = new Date().toISOString();
    await mkdir(dirname(reportPath), {recursive: true});
    const serialized = JSON.stringify(report, null, 2) + '\n';
    if (command === 'start') {
      await writeFile(reportPath, serialized, {mode: 0o600, flag: 'wx'});
    } else {
      // Atomically replace complete JSON so an interrupted check does not
      // truncate the previous 24-hour evidence file.
      const next = reportPath + '.next-' + process.pid;
      await writeFile(next, serialized, {mode: 0o600, flag: 'wx'});
      await rename(next, reportPath);
    }
    console.log('Evidence: ' + reportPath);
  }
}
