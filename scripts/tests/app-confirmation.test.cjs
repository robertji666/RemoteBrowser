const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../../web/static/js/app.js'), 'utf8');

// Isolated event/DOM doubles execute the shipped script, not copied logic.
function setup({missing = false, broken = false} = {}) {
  const handlers = new Map(), elements = new Map();
  const document = {
    activeElement: null,
    addEventListener(name, handler) { handlers.set(name, handler); },
    querySelectorAll() { return []; },
    getElementById(id) { return elements.get(id) || null; },
  };
  function element(id) {
    const events = new Map();
    const el = {isConnected: true, open: false, hidden: true, textContent: '',
      addEventListener: (name, fn) => events.set(name, fn),
      fire(name) { events.get(name)?.({preventDefault() {}}); },
      focus() { document.activeElement = el; },
      showModal() { if (broken) throw Error('unavailable'); el.open = true; },
      close() { el.open = false; el.fire('close'); },
    };
    elements.set(id, el); return el;
  }
  const dialog = missing ? null : element('action-confirm');
  const message = element('action-confirm-message');
  const cancel = element('action-confirm-cancel'), accept = element('action-confirm-accept');
  const error = element('request-error');
  const opener = element('opener'); opener.focus();
  let submissions = 0, requests = 0;
  function event(target, submitter, detail) {
    return {target, submitter, detail, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }};
  }
  const form = {isConnected: true, dataset: {confirm: 'Delete test account?'}, valid: true};
  const button = {isConnected: true, form};
  const HTMLFormElement = {prototype: {requestSubmit(submitter) {
    if (!this.valid) return;
    const e = event(this, submitter); handlers.get('submit')(e);
    if (!e.defaultPrevented) submissions++;
  }}};
  vm.runInNewContext(source, {window: {fetch: async () => ({status: 200})}, document, HTMLFormElement});
  return {dialog, message, cancel, accept, error, opener, document, form, button,
    submit() { const e = event(form, button); handlers.get('submit')(e); return e; },
    htmx(question = 'Delete test instance?') {
      const e = event(button, null, {question, elt: button, issueRequest(skip) { assert.equal(skip, true); requests++; }});
      handlers.get('htmx:confirm')(e); return e;
    },
    counts: () => ({submissions, requests}),
  };
}

test('cancel blocks submission and restores focus', () => {
  const s = setup(); assert.equal(s.submit().defaultPrevented, true);
  assert.equal(s.dialog.open, true); assert.equal(s.document.activeElement, s.cancel);
  assert.equal(s.message.textContent, 'Delete test account?');
  s.cancel.fire('click'); assert.deepEqual(s.counts(), {submissions: 0, requests: 0});
  assert.equal(s.dialog.open, false); assert.equal(s.document.activeElement, s.opener);
});
test('Escape and external dialog close never submit', () => {
  for (const action of ['cancel', 'close']) {
    const s = setup(); s.submit();
    if (action === 'close') s.dialog.close(); else s.dialog.fire('cancel');
    s.accept.fire('click'); assert.equal(s.counts().submissions, 0);
  }
});
test('explicit confirmation submits once without re-prompting', () => {
  const s = setup(); s.submit(); s.accept.fire('click'); s.accept.fire('click');
  assert.equal(s.counts().submissions, 1); assert.equal(s.dialog.open, false);
});
test('missing or failed dialog cannot submit', () => {
  for (const options of [{missing: true}, {broken: true}]) {
    const s = setup(options); assert.equal(s.submit().defaultPrevented, true);
    s.accept.fire('click'); assert.equal(s.counts().submissions, 0);
    if (options.broken) assert.equal(s.error.hidden, false);
  }
});
test('disconnected form, submitter or invalid form cannot submit', () => {
  for (const change of [s => s.form.isConnected = false, s => s.button.isConnected = false, s => s.form.valid = false]) {
    const s = setup(); s.submit(); change(s); s.accept.fire('click');
    assert.equal(s.counts().submissions, 0);
  }
});
test('a pending confirmation cannot be overwritten by another action', () => {
  const s = setup(); s.submit(); assert.equal(s.htmx().defaultPrevented, true);
  s.accept.fire('click'); assert.deepEqual(s.counts(), {submissions: 1, requests: 0});
});
test('htmx cancellation, confirmation and removed source follow the same rules', () => {
  const s = setup(); assert.equal(s.htmx().defaultPrevented, true);
  s.cancel.fire('click'); assert.equal(s.counts().requests, 0);
  s.htmx(); s.accept.fire('click'); assert.equal(s.counts().requests, 1);
  s.htmx(); s.button.isConnected = false; s.accept.fire('click'); assert.equal(s.counts().requests, 1);
});
test('ordinary forms and htmx requests are unaffected', () => {
  const s = setup(); s.form.dataset = {};
  assert.equal(s.submit().defaultPrevented, false);
  assert.equal(s.htmx('').defaultPrevented, false); assert.equal(s.dialog.open, false);
});
