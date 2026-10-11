// Local authenticated proxy for the IAM-protected Cloud Run dashboard. Adds an identity token for
// the authorised account (refreshed every 30 min) and streams responses, so SSE works.
//
// It also serves /local/* itself: the "Destroy lab" button runs scripts/teardown.sh on THIS machine,
// where the Terraform state and the authorised gcloud login live. The cloud control plane cannot do
// that (it would have to delete itself and has no access to the state).
//
// Usage: node dashboard-proxy.mjs <cloud-run-url> <port>   (started by dashboard.sh)
import { execFileSync, spawn } from 'node:child_process';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const [target, port = '8090'] = process.argv.slice(2);
const gcloud = process.env.GCLOUD;
const account = process.env.GCP_ACCOUNT;
const project = process.env.GCP_PROJECT;
const scriptsDir = path.dirname(fileURLToPath(import.meta.url));
export const CONFIRM_PHRASE = 'destroy ticketing-scale-lab';

let token = '';
let fetchedAt = 0;
function identityToken() {
  if (Date.now() - fetchedAt > 30 * 60_000) {
    token = execFileSync(gcloud, [`--account=${account}`, `--project=${project}`, 'auth', 'print-identity-token'], { encoding: 'utf8' }).trim();
    fetchedAt = Date.now();
  }
  return token;
}

const teardown = { state: 'idle', startedAt: null, finishedAt: null, exitCode: null, lines: [] };

function json(res, status, body) {
  res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' });
  res.end(JSON.stringify(body));
}

function startTeardown() {
  Object.assign(teardown, { state: 'running', startedAt: new Date().toISOString(), finishedAt: null, exitCode: null, lines: [] });
  const child = spawn('bash', [path.join(scriptsDir, 'teardown.sh'), '--yes'], { env: process.env, cwd: path.dirname(scriptsDir) });
  const collect = (chunk) => {
    for (const line of chunk.toString().split('\n')) {
      // eslint-disable-next-line no-control-regex
      const clean = line.replace(/\x1b\[[0-9;]*m/g, '').trimEnd();
      if (clean) teardown.lines.push(clean);
    }
    if (teardown.lines.length > 5000) teardown.lines.splice(0, teardown.lines.length - 5000);
  };
  child.stdout.on('data', collect);
  child.stderr.on('data', collect);
  child.on('close', (code) => {
    Object.assign(teardown, { state: code === 0 ? 'succeeded' : 'failed', exitCode: code, finishedAt: new Date().toISOString() });
  });
}

function handleLocal(req, res) {
  if (req.url === '/local/status' && req.method === 'GET') {
    return json(res, 200, { teardown: true, confirmPhrase: CONFIRM_PHRASE, project, account });
  }
  if (req.url === '/local/teardown' && req.method === 'GET') return json(res, 200, teardown);
  if (req.url === '/local/teardown' && req.method === 'POST') {
    // Only the dashboard page itself may trigger this: same-origin Origin header plus a custom
    // header (a cross-site page cannot send it without a CORS preflight, which is never approved).
    const allowed = [`http://localhost:${port}`, `http://127.0.0.1:${port}`];
    if (!allowed.includes(req.headers.origin ?? '')) return json(res, 403, { error: 'forbidden_origin' });
    if (req.headers['x-lab-confirm'] !== CONFIRM_PHRASE) return json(res, 428, { error: 'confirmation_required', confirmPhrase: CONFIRM_PHRASE });
    if (teardown.state === 'running') return json(res, 409, { error: 'already_running' });
    startTeardown();
    return json(res, 202, teardown);
  }
  return json(res, 404, { error: 'not_found' });
}

const upstream = new URL(target);
http.createServer(async (req, res) => {
  if (req.url?.startsWith('/local/')) return handleLocal(req, res);
  try {
    const headers = { ...req.headers, host: upstream.host, authorization: `Bearer ${identityToken()}` };
    delete headers.connection;
    const body = ['GET', 'HEAD'].includes(req.method) ? undefined : req;
    const response = await fetch(new URL(req.url, upstream), { method: req.method, headers, body, duplex: 'half', redirect: 'manual' });
    const out = {};
    response.headers.forEach((v, k) => { if (!['content-encoding', 'content-length', 'transfer-encoding', 'connection'].includes(k)) out[k] = v; });
    res.writeHead(response.status, out);
    if (!response.body) return res.end();
    for await (const chunk of response.body) res.write(chunk);
    res.end();
  } catch (error) {
    if (!res.headersSent) res.writeHead(502, { 'content-type': 'text/plain' });
    res.end(`proxy error: ${error.message}`);
  }
}).listen(Number(port), '127.0.0.1', () => console.log(`Dashboard: http://localhost:${port} → ${target}`));
