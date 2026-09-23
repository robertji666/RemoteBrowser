(() => {
  'use strict';
  const csrf = () => document.querySelector('meta[name="csrf-token"]')?.content || '';
  const originalFetch = window.fetch.bind(window);
  window.fetch = async (input, options = {}) => {
    const url = new URL(input instanceof Request ? input.url : input, location.href);
    const method = (options.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
    if (url.origin === location.origin && !['GET', 'HEAD', 'OPTIONS'].includes(method)) {
      const headers = new Headers(options.headers || (input instanceof Request ? input.headers : undefined));
      headers.set('X-CSRF-Token', csrf()); options = { ...options, headers };
    }
    const response = await originalFetch(input, options);
    if (url.origin === location.origin && response.status === 401 && location.pathname !== '/login') location.assign('/login?notice=' + encodeURIComponent('登录已过期或被撤销，请重新登录。浏览器实例仍然保留。'));
    return response;
  };
  document.addEventListener('htmx:configRequest', e => { e.detail.headers['X-CSRF-Token'] = csrf(); });
  document.addEventListener('htmx:responseError', e => {
    if (e.detail.xhr.status === 401) { location.assign('/login'); return; }
    const box = document.getElementById('request-error');
    if (box) { box.textContent = e.detail.xhr.responseText.slice(0, 500); box.hidden = false; }
  });
  const dialog = document.getElementById('action-confirm');
  const dialogMessage = document.getElementById('action-confirm-message');
  const cancelAction = document.getElementById('action-confirm-cancel');
  const acceptAction = document.getElementById('action-confirm-accept');
  let pendingAction = null;
  function finishConfirmation(accepted) {
    const pending = pendingAction;
    pendingAction = null;
    if (dialog?.open) dialog.close();
    if (pending?.focus?.isConnected) pending.focus.focus();
    if (accepted && pending) pending.perform();
  }
  function confirmAction(message, perform) {
    // Never submit if the dialog is missing, unsupported, or already in use.
    if (pendingAction || !dialog || !dialogMessage || !cancelAction || !acceptAction) return;
    pendingAction = {perform, focus: document.activeElement};
    dialogMessage.textContent = message;
    try {
      dialog.showModal();
      cancelAction.focus();
    } catch {
      finishConfirmation(false);
      const box = document.getElementById('request-error');
      if (box) { box.textContent = '无法显示确认框，操作未提交。请刷新页面后重试。'; box.hidden = false; }
    }
  }
  cancelAction?.addEventListener('click', () => finishConfirmation(false));
  acceptAction?.addEventListener('click', () => finishConfirmation(true));
  dialog?.addEventListener('cancel', e => { e.preventDefault(); finishConfirmation(false); });
  dialog?.addEventListener('close', () => { if (!dialog.open) pendingAction = null; });
  const confirmedForms = new WeakSet();
  document.addEventListener('submit', e => {
    const form = e.target;
    const message = form.dataset.confirm;
    if (!message || confirmedForms.has(form)) return;
    e.preventDefault();
    const submitter = e.submitter;
    confirmAction(message, () => {
      if (!form.isConnected || (submitter && (submitter.form !== form || !submitter.isConnected))) return;
      confirmedForms.add(form);
      try { HTMLFormElement.prototype.requestSubmit.call(form, submitter || undefined); }
      finally { confirmedForms.delete(form); }
    });
  });
  document.addEventListener('htmx:confirm', e => {
    if (!e.detail.question) return;
    e.preventDefault();
    const source = e.detail.elt;
    confirmAction(e.detail.question, () => { if (source.isConnected) e.detail.issueRequest(true); });
  });
  const states = { active:'正常', disabled:'已禁用', deleting:'删除中', delete_failed:'删除失败', creating:'创建中', starting:'启动中', running:'运行中', disconnected:'运行中 · 无连接', stopping:'停止中', stopped:'已停止', error:'异常', unknown:'状态待确认', deleted:'已删除' };
  const deliveries = { manual:'手工交付', sent:'邮件已提交发送', pending:'等待发送', failed:'发送失败', none:'无需交付', '':'—' };
  function enhance(root = document) {
    root.querySelectorAll('[data-state]').forEach(el => { el.textContent = states[el.dataset.state] || el.dataset.state; });
    root.querySelectorAll('[data-delivery]').forEach(el => { el.textContent = deliveries[el.dataset.delivery] || el.dataset.delivery; });
    root.querySelectorAll('time[datetime]').forEach(el => {
      const date = new Date(el.dateTime);
      if (!Number.isNaN(date.valueOf())) { el.textContent = date.toLocaleString(undefined, { year:'numeric', month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit' }); el.title = date.toString(); }
    });
  }
  document.addEventListener('htmx:afterSwap', () => enhance());
  enhance();
})();
