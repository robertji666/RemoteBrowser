// Isolated UI acceptance harness: serves the real dialog markup, CSS, app.js,
// and vendored htmx. Submission endpoints ONLY increment in-memory counters.
// No database, Docker access, credentials or real deletion are involved.
import {createServer} from 'node:http';
import {readFile, mkdir, writeFile} from 'node:fs/promises';
const root = new URL('../', import.meta.url);
const assets = new Map([
  ['/static/js/app.js', ['web/static/js/app.js', 'text/javascript']],
  ['/static/css/app.css', ['web/static/css/app.css', 'text/css']],
  ['/static/vendor/htmx.min.js', ['web/static/vendor/htmx.min.js', 'text/javascript']],
]);
const report = {startedAt: new Date().toISOString(), scope: 'Isolated real browser confirmation requests; no real account deletion', formRequests: 0, htmxRequests: 0, events: []};
const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://127.0.0.1').pathname;
    res.setHeader('Cache-Control', 'no-store');
    if (req.method === 'GET' && assets.has(path)) {
      const [file, type] = assets.get(path); res.setHeader('Content-Type', type);
      res.end(await readFile(new URL(file, root))); return;
    }
    if (req.method === 'GET' && path === '/status') {
      res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(report)); return;
    }
    if ((req.method === 'POST' && path === '/form') || (req.method === 'DELETE' && path === '/instance')) {
      if (path === '/form') report.formRequests++; else report.htmxRequests++;
      report.events.push({at: new Date().toISOString(), method: req.method, path});
      req.resume(); res.setHeader('Content-Type', 'text/html; charset=utf-8');
      res.end(`<p>测试请求已记录；没有删除真实数据。表单 ${report.formRequests} 次，HTMX ${report.htmxRequests} 次。</p><a href="/">返回测试页</a>`); return;
    }
    if (req.method !== 'GET' || path !== '/') { res.writeHead(404); res.end(); return; }
    const base = await readFile(new URL('web/templates/layouts/base.html', root), 'utf8');
    const dialog = base.match(/<dialog\b[\s\S]*?<\/dialog>/)?.[0];
    if (!dialog) throw Error('production dialog missing');
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>确认交互隔离验收</title><link rel="stylesheet" href="/static/css/app.css"><script src="/static/js/app.js" defer></script><script src="/static/vendor/htmx.min.js" defer></script><body class="dark"><main class="dashboard-main narrow"><h1>确认交互隔离验收</h1><p>此页面只记录请求次数，不连接真实账号或实例。</p><div id="request-error" role="alert" hidden></div><section class="panel form-panel"><form method="post" action="/form" data-confirm="确认删除虚构账号 demo-cancel@example.test 及其 0 个实例？"><button class="btn btn-danger">测试删除用户</button></form></section><section class="panel form-panel"><button class="btn btn-danger" hx-delete="/instance" hx-target="#result" hx-confirm="永久删除虚构实例「确认验收」及其数据？">测试删除实例</button><div id="result"></div></section><p>表单请求：${report.formRequests}；HTMX请求：${report.htmxRequests}</p></main>${dialog}</body></html>`);
  } catch { res.writeHead(500); res.end('Fixture unavailable'); }
});
server.listen(0, '127.0.0.1', () => console.log(JSON.stringify({url: `http://127.0.0.1:${server.address().port}`, pid: process.pid})));
async function stop() {
  report.completedAt = new Date().toISOString();
  await mkdir(new URL('data-e2e/', root), {recursive: true});
  await writeFile(new URL('data-e2e/confirmation-preview-requests.json', root), JSON.stringify(report, null, 2) + '\n');
  server.close(() => process.exit(0)); server.closeAllConnections();
}
process.once('SIGTERM', stop); process.once('SIGINT', stop);
