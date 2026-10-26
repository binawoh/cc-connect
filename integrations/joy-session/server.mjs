import fs from 'node:fs';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { DesktopClient } from './mcp-client.mjs';
import { SessionBridge, readJSON, watchJob } from './router.mjs';

const filename = fileURLToPath(import.meta.url);
const [mode = 'serve', settingsFile = process.env.JOY_SESSION_CONFIG, jobId] = process.argv.slice(2);
if (!settingsFile) throw new Error('JOY_SESSION_CONFIG is required.');
const settings = readJSON(settingsFile);
const jobsDirectory = path.join(settings.stateDirectory, 'jobs');
fs.mkdirSync(jobsDirectory, { recursive: true });

function alive(pid) {
  if (!Number.isSafeInteger(pid) || pid < 1) return false;
  try { process.kill(pid, 0); return true; } catch (error) { return error.code === 'EPERM'; }
}

function startWatcher(id) {
  if (!/^[0-9a-f-]{36}$/.test(id)) throw new Error('Invalid job id.');
  const lock = readJSON(path.join(jobsDirectory, `${id}.lock`), null);
  if (lock && alive(lock.pid)) return;
  const log = fs.openSync(path.join(jobsDirectory, `${id}.log`), 'a');
  try {
    const child = spawn(process.execPath, [filename, 'watch', settingsFile, id], { detached: true, windowsHide: true, stdio: ['ignore', log, log] });
    child.on('error', error => process.stderr.write(`Workspace watcher failed: ${error.message}\n`));
    child.unref();
  } finally { fs.closeSync(log); }
}

const tools = [
  {
    name: 'send_task',
    description: 'Send boss\'s task or follow-up ONLY to the single existing desktop session selected in configuration. There is no target selector and no new session or subagent. Preserves that session\'s settings. Return after submission; the background bridge sends its final answer to WeChat. Do not wait or automatically resend an uncertain submission.',
    inputSchema: { type: 'object', properties: { message: { type: 'string', description: 'Boss\'s task and necessary context, without adding authority.' } }, required: ['message'], additionalProperties: false },
  },
  {
    name: 'task_status',
    description: 'Read the fixed target and the latest task\'s progress, result and WeChat delivery status. Resumes observation after a desktop disconnect without resending the task.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
];

async function runTool(name, args, metadata) {
  if (!tools.some(tool => tool.name === name)) throw new Error(`Unknown workspace tool: ${name}`);
  const allowed = name === 'send_task' ? ['message'] : [];
  if (Object.keys(args).some(key => !allowed.includes(key))) throw new Error('The target is fixed; only the task message can be supplied.');
  const client = new DesktopClient(settings, metadata);
  const router = new SessionBridge(settings, client, metadata, startWatcher);
  if (name === 'task_status') return router.status();
  try {
    await client.start();
    return await router.dispatch(args.message);
  } finally { await client.close(); }
}

async function worker() {
  if (!/^[0-9a-f-]{36}$/.test(jobId ?? '')) throw new Error('Invalid watcher job id.');
  const lockFile = path.join(jobsDirectory, `${jobId}.lock`);
  const previous = readJSON(lockFile, null);
  if (previous && alive(previous.pid)) return;
  if (previous) fs.unlinkSync(lockFile);
  let lock;
  try { lock = fs.openSync(lockFile, 'wx', 0o600); }
  catch (error) { if (error.code === 'EEXIST') return; throw error; }
  fs.writeFileSync(lock, JSON.stringify({ pid: process.pid }));
  fs.closeSync(lock);
  const job = readJSON(path.join(jobsDirectory, `${jobId}.json`));
  const client = new DesktopClient(settings, job.metadata);
  const router = new SessionBridge(settings, client, job.metadata, startWatcher);
  if (job.threadId !== settings.targetThreadId) throw new Error('Watcher target differs from the configured session.');
  try {
    await client.start();
    await watchJob(router, jobId);
  } catch (error) {
    job.status = 'waiting_desktop'; job.error = error.message; router.saveJob(job);
  } finally {
    await client.close();
    if (readJSON(lockFile, null)?.pid === process.pid) fs.unlinkSync(lockFile);
  }
}

async function serve() {
  // Resume observation only. Never replay send_message_to_thread on startup.
  for (const file of fs.readdirSync(jobsDirectory).filter(name => name.endsWith('.json'))) {
    const job = readJSON(path.join(jobsDirectory, file));
    if (job.threadId === settings.targetThreadId && ['waiting', 'waiting_desktop', 'running'].includes(job.status)) startWatcher(job.id);
  }
  const lines = createInterface({ input: process.stdin });
  lines.on('line', async line => {
    let request;
    try { request = JSON.parse(line); } catch { return; }
    if (request.id === undefined) return;
    let result;
    try {
      if (request.method === 'initialize') result = { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: 'joy-session', version: '1.0.0' } };
      else if (request.method === 'ping') result = {};
      else if (request.method === 'tools/list') result = { tools };
      else if (request.method === 'tools/call') {
        const data = await runTool(request.params.name, request.params.arguments ?? {}, request.params._meta ?? {});
        result = { content: [{ type: 'text', text: JSON.stringify(data) }] };
      } else throw new Error(`Unsupported MCP method: ${request.method}`);
    } catch (error) { result = { isError: true, content: [{ type: 'text', text: error.message }] }; }
    process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: request.id, result })}\n`);
  });
}

if (mode === 'watch') await worker();
else if (mode === 'serve') await serve();
else throw new Error('Mode must be serve or watch.');
