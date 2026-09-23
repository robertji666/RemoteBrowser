// Runs only against a disposable local deployment with explicitly supplied
// bootstrap credentials. It creates e2e-*@example.test users and leaves two
// instances for browser/persistence checks. No external email is sent.
import assert from 'node:assert/strict';
import {readFile, mkdir, writeFile} from 'node:fs/promises';
import {execFileSync} from 'node:child_process';
import {existsSync} from 'node:fs';

const origin = process.env.RB_E2E_URL || 'http://localhost';
assert(['localhost', '127.0.0.1'].includes(new URL(origin).hostname), 'local test target required');
const env = await readFile('.env', 'utf8');
const adminPassword = process.env.RB_E2E_ADMIN_PASSWORD || env.match(/^RB_ADMIN_PASSWORD=(.+)$/m)?.[1];
assert(adminPassword, 'supply test administrator password');
const run = Date.now().toString(36);
const report = {run, startedAt: new Date().toISOString(), checks: [], users: [], instances: []};
const temporaryPassword = 'RB-e2e-temporary-2026!';
const userPassword = 'RB-e2e-permanent-2026!';
const resetPassword = 'RB-e2e-reset-2026!';
const record = (name) => { report.checks.push({name, at: new Date().toISOString()}); console.log('PASS ' + name); };
const docker = (...args) => execFileSync('docker', args, {encoding: 'utf8', timeout: 30000}).trim();
const resources = id => {
  const c = JSON.parse(docker('inspect', 'rb-sess-' + id))[0];
  return {name: c.Name.slice(1), paths: c.Mounts.filter(m => m.Type === 'bind').map(m => m.Source), networks: Object.keys(c.NetworkSettings.Networks)};
};
function assertRemoved(r) {
  assert(!docker('ps', '-a', '--format', '{{.Names}}').split('\n').includes(r.name));
  for (const path of r.paths) assert(!existsSync(path), 'instance data remains: ' + path);
  const networks = docker('network', 'ls', '--format', '{{.Name}}').split('\n');
  for (const network of r.networks) assert(!networks.includes(network), 'instance network remains: ' + network);
}
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));

class Client {
  cookies = new Map();
  csrf = '';
  async request(path, {method = 'GET', form, body, headers = {}} = {}) {
    const merged = {Accept: 'application/json', ...headers};
    if (this.cookies.size) merged.Cookie = [...this.cookies].map(([k, v]) => k + '=' + v).join('; ');
    if (method !== 'GET') merged['X-CSRF-Token'] = this.csrf;
    if (form) body = new URLSearchParams(form);
    const response = await fetch(origin + path, {method, headers: merged, body, redirect: 'manual', signal: AbortSignal.timeout(100000)});
    for (const cookie of response.headers.getSetCookie()) {
      const first = cookie.split(';')[0]; const i = first.indexOf('=');
      this.cookies.set(first.slice(0, i), first.slice(i + 1));
    }
    const text = await response.text();
    let data;
    try { data = JSON.parse(text); } catch {}
    return {status: response.status, data, text, location: response.headers.get('location')};
  }
  async login(email, password) {
    await this.request('/login');
    this.csrf = this.cookies.get('rb_csrf');
    const r = await this.request('/api/login', {method: 'POST', form: {username: email, password}});
    assert.equal(r.status, 303, 'login ' + email + ': ' + r.text.slice(0, 250));
  }
  async post(path, form = {}) {
    return this.request(path, {method: 'POST', form});
  }
}
const expect = (r, status = 200) => { assert.equal(r.status, status, r.text.slice(0, 300)); return r.data; };
const admin = new Client();
async function newUser(label, quota) {
  const email = 'e2e-' + label + '-' + run + '@example.test';
  const u = expect(await admin.post('/api/admin/users', {email, display_name: '验收测试 ' + label, quota: String(quota), manual: 'true', password: temporaryPassword}));
  const c = new Client();
  await c.login(email, temporaryPassword);
  assert.equal((await c.request('/api/sessions')).status, 403);
  const first = {...c, cookies: new Map(c.cookies)};
  expect(await c.post('/api/account/password', {current_password: temporaryPassword, new_password: userPassword, confirm_password: userPassword}));
  assert.equal((await c.request('/api/account')).status, 401);
  await c.login(email, userPassword);
  report.users.push({id: u.id, email, label});
  record(label + ': email login, forced first change, token revocation');
  return {u, c, oldCookies: first.cookies};
}
async function ready(c, id) {
  for (let i = 0; i < 100; i++) {
    const s = expect(await c.request('/api/sessions/' + id));
    if (s.status === 'running') return s;
    if (s.status === 'error') throw Error('instance launch failed: ' + s.lastError);
    await pause(1000);
  }
  throw Error('instance did not become ready: ' + id);
}
async function create(c, name, key) {
  const s = expect(await c.post('/api/sessions', {name, request_key: key}), 201);
  return ready(c, s.id);
}
async function file(c, id, name, content) {
  const body = new FormData();
  body.append('file', new Blob([content], {type: 'text/plain'}), name);
  expect(await c.request('/api/sessions/' + id + '/files/upload', {method: 'POST', body}));
  const result = await c.request('/api/sessions/' + id + '/files/' + encodeURIComponent(name));
  assert.equal(result.status, 200);
  assert.equal(result.text, content);
}
async function deleteUser(u) {
  const bad = await admin.post('/api/admin/users/' + u.id + '/delete', {confirm_email: u.email});
  assert.equal(bad.status, 400, 'missing confirmation must not delete');
  expect(await admin.post('/api/admin/users/' + u.id + '/delete', {confirm_email: u.email, confirm_delete: 'yes'}));
}

try {
  await admin.login('admin', adminPassword);
  record('fresh admin bootstrap and login');
  const {u: alice, c: a} = await newUser('alice', 2);
  const {u: bob, c: b} = await newUser('bob', 1);
  const A = await create(a, '长期保留测试 A', 'a-' + run);
  const duplicate = expect(await a.post('/api/sessions', {name: 'duplicate', request_key: 'a-' + run}), 201);
  assert.equal(duplicate.id, A.id);
  record('same creation request is idempotent');
  const concurrent = await Promise.all(Array.from({length: 5}, (_, i) => a.post('/api/sessions', {name: '并发名额 ' + i, request_key: 'parallel-' + run + '-' + i})));
  assert.equal(concurrent.filter(r => r.status === 201).length, 1);
  const A2 = await ready(a, concurrent.find(r => r.status === 201).data.id);
  assert.equal(expect(await a.request('/api/sessions')).length, 2);
  record('five concurrent creates compete for exactly one remaining slot');
  const B = await create(b, '隔离与文件测试 B', 'b-' + run);
  report.instances.push({id: A.id, owner: alice.id}, {id: B.id, owner: bob.id});

  const unauthorized = [
    ['GET', '/api/sessions/' + A.id], ['POST', '/api/sessions/' + A.id + '/start'],
    ['POST', '/api/sessions/' + A.id + '/stop'], ['DELETE', '/api/sessions/' + A.id],
    ['GET', '/api/sessions/' + A.id + '/files'], ['POST', '/api/sessions/' + A.id + '/files/upload'],
    ['GET', '/api/sessions/' + A.id + '/files/secret.txt'], ['POST', '/api/sessions/' + A.id + '/clipboard/push'],
    ['GET', '/api/sessions/' + A.id + '/clipboard/pull'], ['GET', '/sessions/' + A.id + '/view'],
    ['GET', '/sessions/' + A.id + '/novnc/vnc.html'], ['GET', '/sessions/' + A.id + '/audio'],
    ['GET', '/sessions/' + A.id + '/input'], ['GET', '/sessions/' + A.id + '/input/state'],
    ['POST', '/sessions/' + A.id + '/webrtc/offer']
  ];
  for (const who of [b, admin]) for (const [method, path] of unauthorized) {
    assert.equal((await who.request(path, {method})).status, 404, method + ' ' + path);
  }
  assert.equal((await b.request('/api/admin/users')).status, 403);
  assert.equal((await b.post('/api/admin/users', {})).status, 403);
  assert(!expect(await b.request('/api/sessions')).some(s => s.id === A.id));
  record('all 15 instance surfaces deny other user and admin; ordinary user denied administration');
  await file(a, A.id, '持久化验收.txt', 'RemoteBrowser 中文持久化 ' + run);
  await file(b, B.id, '隔离验收.txt', 'Only user B ' + run);
  assert(!expect(await a.request('/api/sessions/' + A.id + '/files')).some(f => f.name === '隔离验收.txt'));
  record('real file upload/download, Unicode names, and file isolation');

  expect(await a.post('/api/sessions/' + A2.id + '/stop'));
  assert.equal(expect(await a.request('/api/sessions/' + A2.id)).status, 'stopped');
  assert.equal((await a.post('/api/sessions', {name: 'over quota', request_key: 'over-' + run})).status, 400);
  expect(await admin.post('/api/admin/users/' + alice.id + '/quota', {quota: '1'}));
  assert.equal(expect(await a.request('/api/sessions')).length, 2);
  assert.equal((await a.post('/api/sessions', {name: 'lower quota', request_key: 'lower-' + run})).status, 400);
  const counted = expect(await admin.request('/api/admin/users?q=' + encodeURIComponent(alice.email)));
  assert.equal(counted.users[0].instanceCount, 2);
  record('stopped instance counts; lowering quota preserves both resources and blocks new creates');
  const a2Resources = resources(A2.id);
  expect(await a.request('/api/sessions/' + A2.id, {method: 'DELETE'}));
  assertRemoved(a2Resources);
  record('instance deletion confirms container and data cleanup before releasing quota');
  expect(await a.post('/api/sessions/' + A.id + '/stop'));
  expect(await a.post('/api/sessions/' + A.id + '/start'));
  const persisted = await a.request('/api/sessions/' + A.id + '/files/' + encodeURIComponent('持久化验收.txt'));
  assert.equal(persisted.text, 'RemoteBrowser 中文持久化 ' + run);
  record('manual stop/start preserves identity and download contents');

  expect(await admin.post('/api/admin/users/' + bob.id + '/status', {status: 'disabled'}));
  assert.equal((await b.request('/api/account')).status, 401);
  assert.equal(docker('inspect', 'rb-sess-' + B.id, '--format', '{{.State.Running}}'), 'true');
  expect(await admin.post('/api/admin/users/' + bob.id + '/status', {status: 'active'}));
  await b.login(bob.email, userPassword);
  expect(await admin.post('/api/admin/users/' + bob.id + '/password', {manual: 'true', password: resetPassword, confirm_password: resetPassword}));
  assert.equal((await b.request('/api/account')).status, 401);
  expect(await a.request('/api/account'));
  assert.equal(docker('inspect', 'rb-sess-' + B.id, '--format', '{{.State.Running}}'), 'true');
  await b.login(bob.email, resetPassword);
  expect(await b.post('/api/account/password', {current_password: resetPassword, new_password: userPassword, confirm_password: userPassword}));
  await b.login(bob.email, userPassword);
  record('disable/enable/admin reset revoke target only and keep running instance');

  const {u: disposable, c: d} = await newUser('delete', 1);
  const D = await create(d, '只用于删除验收', 'delete-' + run);
  await file(d, D.id, 'delete-me.txt', 'disposable test');
  const dResources = resources(D.id);
  await deleteUser(disposable);
  assert.equal((await d.request('/api/account')).status, 401);
  assertRemoved(dResources);
  assert.equal(expect(await admin.request('/api/admin/users?q=' + encodeURIComponent(disposable.email))).total, 0);
  const reborn = expect(await admin.post('/api/admin/users', {email: disposable.email, quota: '0', manual: 'true', password: temporaryPassword}));
  assert.notEqual(reborn.id, disposable.id);
  await deleteUser(reborn);
  record('two-confirmation user cascade deletion; email reuse gets a fresh identity');
  const protection = await admin.post('/api/admin/users/1/delete', {confirm_email: '', confirm_delete: 'yes'});
  assert.equal(protection.status, 400);
  record('built-in administrator cannot be deleted');
  report.completedAt = new Date().toISOString();
  report.result = 'passed';
} catch (error) {
  report.result = 'failed';
  report.error = error.message;
  process.exitCode = 1;
  console.error(error.stack);
} finally {
  await mkdir('data-e2e', {recursive: true});
  await writeFile('data-e2e/platform-report.json', JSON.stringify(report, null, 2) + '\n', {mode: 0o600});
  console.log('Report: data-e2e/platform-report.json');
}
