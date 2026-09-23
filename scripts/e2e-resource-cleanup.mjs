// Run from the repository root against the disposable local deployment after
// e2e-platform.mjs. Creates one uniquely named user and three browser instances,
// then deletes only that user through the platform's confirmed deletion API.
// Existing A/B instances are read-only witnesses. No database edits, password
// resets of existing users, network removals, or direct directory deletions.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createHash, randomBytes} from 'node:crypto';
import {existsSync} from 'node:fs';
import {mkdir, readFile, readdir, stat, writeFile} from 'node:fs/promises';
import {dirname, isAbsolute, join, resolve} from 'node:path';

const origin = process.env.RB_E2E_URL || 'http://localhost';
const target = new URL(origin);
assert(['localhost', '127.0.0.1'].includes(target.hostname), 'local test target required');
assert(!target.username && !target.password && !target.search && !target.hash, 'plain local origin required');
const run = Date.now().toString(36) + '-' + randomBytes(3).toString('hex');
const reportPath = process.env.RB_E2E_CLEANUP_REPORT || 'data-e2e/resource-cleanup-report.json';
assert.notEqual(resolve(reportPath), resolve('data-e2e/platform-report.json'), 'cleanup report must not overwrite protected platform report');
const report = {run, startedAt: new Date().toISOString(), result: 'running', checks: [],
  createdUser: null, instances: [], protectedInstances: [], limitations: [
    'The third instance is stopped out-of-band with Docker. Automatic recovery may race this stop; no database state is fabricated.',
    'A persisted error state and cleanup-failure retry are covered by lifecycle unit tests, not injected by this script.',
    'Other-user data checks compare stable download fixtures and profile-directory identity; live Chromium cache files are not expected to remain byte-identical.',
  ]};
const temporaryPassword = randomBytes(24).toString('base64url');
const userPassword = randomBytes(24).toString('base64url');
const record = name => {
  report.checks.push({name, at: new Date().toISOString()});
  console.log('PASS ' + name);
};
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const docker = (...args) => execFileSync('docker', args, {
  encoding: 'utf8', timeout: 45000, stdio: ['ignore', 'pipe', 'pipe'],
}).trim();
const inspect = name => JSON.parse(docker('inspect', name))[0];
const readValue = (source, key) => {
  const raw = source.split(/\r?\n/).find(line => line.startsWith(key + '='))?.slice(key.length + 1).trim();
  if (!raw) return '';
  if ((raw.startsWith('"') && raw.endsWith('"')) || (raw.startsWith("'") && raw.endsWith("'"))) return raw.slice(1, -1);
  return raw.replace(/\s+#.*$/, '');
};

class Client {
  cookies = new Map();
  csrf = '';
  async request(path, {method = 'GET', form, body, headers = {}} = {}) {
    const merged = {Accept: 'application/json', ...headers};
    if (this.cookies.size) merged.Cookie = [...this.cookies].map(([k, v]) => k + '=' + v).join('; ');
    if (method !== 'GET') merged['X-CSRF-Token'] = this.csrf;
    if (form) body = new URLSearchParams(form);
    const response = await fetch(origin + path, {method, headers: merged, body,
      redirect: 'manual', signal: AbortSignal.timeout(150000)});
    for (const cookie of response.headers.getSetCookie()) {
      const first = cookie.split(';')[0];
      const i = first.indexOf('=');
      this.cookies.set(first.slice(0, i), first.slice(i + 1));
    }
    const text = await response.text();
    let data;
    try { data = JSON.parse(text); } catch {}
    return {status: response.status, data, text, path, method};
  }
  async login(email, password) {
    await this.request('/login');
    this.csrf = this.cookies.get('rb_csrf');
    const r = await this.post('/api/login', {username: email, password});
    assert.equal(r.status, 303, 'login failed for test account');
  }
  post(path, form = {}) { return this.request(path, {method: 'POST', form}); }
}
const expect = (response, status = 200) => {
  assert.equal(response.status, status, response.method + ' ' + response.path + ': unexpected HTTP status');
  return response.data;
};

function snapshotResources(id, owner) {
  assert.match(id, /^sess_[a-f0-9]+$/, 'expected generated instance ID');
  const container = inspect('rb-sess-' + id);
  const labels = container.Config.Labels || {};
  assert.equal(labels['remotebrowser.session.id'], id, 'container instance ownership');
  assert.equal(labels['remotebrowser.user.id'], String(owner), 'container user ownership');
  assert.equal(labels['remotebrowser.managed'], 'true', 'managed container required');
  const binds = container.Mounts.filter(m => m.Type === 'bind')
    .map(m => ({source: m.Source, destination: m.Destination})).sort((a, b) => a.destination.localeCompare(b.destination));
  assert.equal(binds.length, 2, 'expected only profile and downloads bind mounts');
  const profile = binds.find(m => m.destination === '/home/rbuser/profile');
  const downloads = binds.find(m => ['/home/rbuser/Downloads', '/home/rbuser/downloads'].includes(m.destination));
  assert(profile && downloads, 'persistent bind mount destinations required');
  for (const mount of binds) {
    assert(isAbsolute(mount.source), 'absolute bind source required');
    assert.equal(dirname(dirname(mount.source)), join(hostDataDir, 'sessions'), 'bind source belongs to configured deployment');
    assert.equal(dirname(mount.source), join(hostDataDir, 'sessions', id), 'bind source belongs to exact instance');
  }
  const networks = Object.keys(container.NetworkSettings.Networks).sort().map(name => {
    const network = JSON.parse(docker('network', 'inspect', name))[0];
    assert.equal(network.Labels['remotebrowser.session.id'], id, 'dedicated network instance ownership');
    assert.equal(network.Labels['remotebrowser.user.id'], String(owner), 'dedicated network user ownership');
    assert.equal(network.Labels['remotebrowser.network.role'], 'instance', 'dedicated instance network required');
    return {id: network.Id, name};
  });
  assert.equal(networks.length, 1, 'one isolated instance network required');
  return {id, owner, containerID: container.Id, containerName: container.Name.slice(1),
    running: container.State.Running, binds, profile: profile.source, downloads: downloads.source, networks};
}

async function snapshotWitness(instance, label, platformRun) {
  const current = snapshotResources(instance.id, instance.owner);
  assert(current.running, 'protected ' + label + ' instance must be running before test');
  const filename = label === 'alice' ? '持久化验收.txt' : '隔离验收.txt';
  const expected = label === 'alice' ? 'RemoteBrowser 中文持久化 ' + platformRun : 'Only user B ' + platformRun;
  const filePath = join(current.downloads, filename);
  const content = await readFile(filePath);
  assert.equal(content.toString('utf8'), expected, 'protected fixture must match platform report');
  const info = await stat(current.profile);
  assert(info.isDirectory(), 'protected profile directory exists');
  return {...current, label, fixture: {path: filePath, sha256: hash(content)}, profileIdentity: {dev: info.dev, ino: info.ino}};
}

async function assertWitnessUnchanged(before) {
  const after = snapshotResources(before.id, before.owner);
  assert(after.running, 'protected ' + before.label + ' instance stopped');
  assert.equal(after.containerID, before.containerID, 'protected container was replaced');
  assert.deepEqual(after.binds, before.binds, 'protected mounts changed');
  assert.deepEqual(after.networks, before.networks, 'protected networks changed');
  assert.equal(hash(await readFile(before.fixture.path)), before.fixture.sha256, 'protected file content changed');
  const info = await stat(before.profile);
  assert.deepEqual({dev: info.dev, ino: info.ino}, before.profileIdentity, 'protected profile directory replaced');
}

async function ready(client, id) {
  for (let i = 0; i < 130; i++) {
    const instance = expect(await client.request('/api/sessions/' + id));
    if (instance.status === 'running') return instance;
    assert.notEqual(instance.status, 'error', 'instance startup failed: ' + id + ': ' + instance.lastError);
    await pause(1000);
  }
  throw new Error('instance startup timed out: ' + id);
}

async function assertRemoved(resources) {
  // Docker list failures throw; a daemon outage is never interpreted as absence.
  const containers = docker('ps', '-a', '--no-trunc', '--format', '{{.ID}} {{.Names}}').split('\n');
  assert(!containers.some(line => line.includes(resources.containerID) || line.endsWith(' ' + resources.containerName)), 'container remains after successful deletion');
  for (const mount of resources.binds) assert(!existsSync(mount.source), 'bind directory remains: ' + mount.source);
  assert(!existsSync(dirname(resources.profile)), 'instance parent directory remains');
  const networks = docker('network', 'ls', '--no-trunc', '--format', '{{.ID}} {{.Name}}').split('\n');
  for (const network of resources.networks) {
    assert(!networks.some(line => line.startsWith(network.id + ' ') || line.endsWith(' ' + network.name)), 'dedicated network remains: ' + network.name);
  }
}

let hostDataDir;
try {
  const env = await readFile('.env', 'utf8');
  const adminPassword = process.env.RB_E2E_ADMIN_PASSWORD || readValue(env, 'RB_ADMIN_PASSWORD');
  assert(adminPassword, 'test administrator password required');
  const rawHostDir = process.env.RB_SESSION_HOST_DATA_DIR || readValue(env, 'RB_SESSION_HOST_DATA_DIR');
  assert(rawHostDir && isAbsolute(rawHostDir), 'configured absolute host data directory required');
  hostDataDir = resolve(rawHostDir);
  const platform = JSON.parse(await readFile('data-e2e/platform-report.json', 'utf8'));
  assert.equal(platform.result, 'passed', 'successful platform report required before destructive cleanup test');
  for (const label of ['alice', 'bob']) {
    const user = platform.users.find(u => u.label === label);
    assert(user, 'platform report missing protected ' + label + ' user');
    const instance = platform.instances.find(s => s.owner === user.id);
    assert(instance, 'platform report missing protected ' + label + ' instance');
    report.protectedInstances.push(await snapshotWitness(instance, label, platform.run));
  }
  record('existing A/B running containers, dedicated networks, profile identity and fixture hashes recorded');

  const admin = new Client();
  await admin.login('admin', adminPassword);
  const email = 'e2e-cleanup-' + run + '@example.test';
  const user = expect(await admin.post('/api/admin/users', {email, display_name: '三实例级联清理 ' + run,
    quota: '3', manual: 'true', password: temporaryPassword}));
  assert(!platform.users.some(existing => existing.id === user.id), 'cleanup user must have a fresh ID');
  report.createdUser = {id: user.id, email};
  const client = new Client();
  await client.login(email, temporaryPassword);
  expect(await client.request('/api/sessions'), 403);
  expect(await client.post('/api/account/password', {current_password: temporaryPassword,
    new_password: userPassword, confirm_password: userPassword}));
  expect(await client.request('/api/account'), 401);
  await client.login(email, userPassword);
  record('unique quota-3 cleanup user completed forced first password change');

  for (const scenario of ['running', 'api-stopped', 'externally-stopped']) {
    const created = expect(await client.post('/api/sessions', {name: '级联清理 ' + scenario,
      request_key: 'cleanup-' + run + '-' + scenario}), 201);
    assert(!report.protectedInstances.some(p => p.id === created.id), 'cleanup target is not protected instance');
    report.instances.push({id: created.id, scenario});
    await ready(client, created.id);
    const resources = snapshotResources(created.id, user.id);
    Object.assign(report.instances.at(-1), resources);
    const body = new FormData();
    const content = 'cleanup-only marker ' + run + ' ' + scenario;
    body.append('file', new Blob([content], {type: 'text/plain'}), 'cleanup-only.txt');
    expect(await client.request('/api/sessions/' + created.id + '/files/upload', {method: 'POST', body}));
    assert.equal(await readFile(join(resources.downloads, 'cleanup-only.txt'), 'utf8'), content, 'real persisted cleanup fixture');
    for (let attempt = 0; attempt < 20 && (await readdir(resources.profile)).length === 0; attempt++) await pause(500);
    assert((await readdir(resources.profile)).length > 0, 'profile must contain browser data before cleanup');
  }
  assert.equal(expect(await client.request('/api/sessions')).length, 3);
  const stopped = report.instances.find(s => s.scenario === 'api-stopped');
  expect(await client.post('/api/sessions/' + stopped.id + '/stop'));
  assert.equal(expect(await client.request('/api/sessions/' + stopped.id)).status, 'stopped');
  assert.equal(inspect(stopped.containerName).State.Running, false);
  const running = report.instances.find(s => s.scenario === 'running');
  assert.equal(inspect(running.containerName).State.Running, true);
  record('three real resource sets exist; running and API-stopped instances both occupy quota');

  const deletedPath = '/api/admin/users/' + user.id + '/delete';
  expect(await admin.post(deletedPath, {confirm_email: email}), 400);
  expect(await admin.post(deletedPath, {confirm_email: 'wrong-' + email, confirm_delete: 'yes'}), 400);
  const beforeDelete = expect(await admin.request('/api/admin/users?q=' + encodeURIComponent(email)));
  const listed = beforeDelete.users.find(u => u.id === user.id);
  assert.equal(listed?.status, 'active', 'rejected confirmation must not freeze account');
  assert.equal(listed?.instanceCount, 3, 'rejected confirmation must preserve every instance');
  expect(await client.request('/api/account'));
  for (const instance of report.instances) inspect(instance.containerName);
  record('missing checkbox and wrong email reject deletion without changing account or resources');

  const unexpected = report.instances.find(s => s.scenario === 'externally-stopped');
  // Exact container ownership was verified above; only the newly created test
  // container is stopped. Reconciliation is allowed to restore desired running.
  docker('stop', '--time', '10', unexpected.containerName);
  report.externalStopObserved = {containerRunning: inspect(unexpected.containerName).State.Running,
    platformState: expect(await client.request('/api/sessions/' + unexpected.id)).status};
  record('third owned container stopped out-of-band; actual observed state recorded without database edits');

  expect(await admin.post(deletedPath, {confirm_email: email, confirm_delete: 'yes'}));
  for (const resources of report.instances) await assertRemoved(resources);
  expect(await client.request('/api/account'), 401);
  expect(await client.request('/api/sessions'), 401);
  const deletedListing = expect(await admin.request('/api/admin/users?q=' + encodeURIComponent(email)));
  assert(!deletedListing.users.some(u => u.id === user.id), 'deleted user remains in administration');
  const fresh = new Client();
  await fresh.request('/login');
  fresh.csrf = fresh.cookies.get('rb_csrf');
  expect(await fresh.post('/api/login', {username: email, password: userPassword}), 401);
  record('confirmed user deletion removed three containers, all six bind directories, parent directories and dedicated networks; credentials denied');

  for (const witness of report.protectedInstances) await assertWitnessUnchanged(witness);
  record('protected A/B remain the same running containers with unchanged mounts, networks, profile identity and download contents');
  report.result = 'passed';
  report.completedAt = new Date().toISOString();
} catch (error) {
  report.result = 'failed';
  report.error = error.message;
  process.exitCode = 1;
  console.error(error.message);
  // Keep the exact created IDs for diagnosis/retry rather than attempting
  // an unreported second destructive path after an assertion failure.
} finally {
  await mkdir(dirname(reportPath), {recursive: true});
  await writeFile(reportPath, JSON.stringify(report, null, 2) + '\n', {mode: 0o600});
  console.log('Report: ' + reportPath);
}
