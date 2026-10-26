import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';

export function workspaceKey(value) {
  if (!path.isAbsolute(value)) throw new Error('Use an absolute workspace path.');
  const real = fs.realpathSync.native(value).replace(/^\\\\\?\\/, '');
  if (!fs.statSync(real).isDirectory()) throw new Error('Workspace is not a directory.');
  return process.platform === 'win32' ? real.toLowerCase() : real;
}

export function readJSON(file, fallback) {
  try { return JSON.parse(fs.readFileSync(file, 'utf8').replace(/^\uFEFF/, '')); }
  catch (error) { if (error.code === 'ENOENT' && fallback !== undefined) return fallback; throw error; }
}

export function writeJSON(file, data) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const temporary = `${file}.${process.pid}.${randomUUID()}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(data, null, 2), { mode: 0o600 });
  fs.renameSync(temporary, file);
}

export function callerThread(metadata = {}) {
  for (const key of ['openai/threadId', 'openai/thread_id', 'codexThreadId', 'codex_thread_id', 'threadId', 'thread_id']) {
    if (typeof metadata[key] === 'string' && metadata[key]) return metadata[key];
  }
  let turn = metadata['x-codex-turn-metadata'];
  if (typeof turn === 'string') { try { turn = JSON.parse(turn); } catch { turn = null; } }
  return turn?.thread_id || metadata.thread?.id;
}

export class SessionBridge {
  constructor(settings, client, metadata, startWatcher = () => {}) {
    this.settings = settings;
    this.client = client;
    this.metadata = metadata;
    this.startWatcher = startWatcher;
    this.stateFile = path.join(settings.stateDirectory, 'state.json');
  }

  state() { return readJSON(this.stateFile, {}); }
  jobPath(id) {
    if (!/^[0-9a-f-]{36}$/.test(id)) throw new Error('Invalid job id.');
    return path.join(this.settings.stateDirectory, 'jobs', `${id}.json`);
  }
  saveJob(job) { writeJSON(this.jobPath(job.id), job); }
  getJob(id) { return readJSON(this.jobPath(id)); }
  assertCaller() {
    if (callerThread(this.metadata) !== this.settings.joyThreadId) throw new Error('Only the configured Joy main session may send tasks.');
  }

  async verifyTarget() {
    const threadId = this.settings.targetThreadId;
    const workspace = this.settings.targetWorkspace;
    if (!threadId || threadId === this.settings.joyThreadId) throw new Error('Joy cannot dispatch work to its own main session.');
    const response = await this.client.call('read_thread', { hostId: 'local', threadId, turnLimit: 1, includeOutputs: false, maxOutputCharsPerItem: 300 });
    const thread = response.thread;
    if (thread?.id !== threadId || thread.kind !== 'codex' || thread.hostId !== 'local' || workspaceKey(thread.cwd) !== workspaceKey(workspace)) {
      throw new Error('The configured session no longer matches the fixed workspace; request not sent.');
    }
    return { workspace: thread.cwd, threadId, hostId: 'local', title: thread.title };
  }

  async dispatch(message) {
    this.assertCaller();
    if (!message?.trim()) throw new Error('A user-authorized task message is required.');
    const target = await this.verifyTarget();
    const id = randomUUID();
    const job = { id, ...target, metadata: this.metadata, message, marker: `[Joy request ${id}]`, createdAt: new Date().toISOString(), status: 'sending', delivery: 'not_sent' };
    this.saveJob(job);
    writeJSON(this.stateFile, { latestJobId: id });
    try {
      job.sendResponse = await this.client.call('send_message_to_thread', {
        hostId: target.hostId, threadId: target.threadId,
        prompt: `${job.marker}\n来自微信 boss，由 Joy 转交，请在这个已有会话中继续处理：\n\n${message}\n\n最终结果会由桥接自动回到微信，无需另行发送消息。`,
      });
    } catch (error) {
      job.status = 'send_unknown';
      job.error = error.message;
      this.saveJob(job);
      throw new Error(`Desktop submission was not confirmed. Do not resend automatically. Job ${id}: ${error.message}`);
    }
    job.status = 'waiting';
    this.saveJob(job);
    try { this.startWatcher(id); }
    catch (error) { job.error = `Submission accepted, watcher not started: ${error.message}`; this.saveJob(job); }
    return { accepted: true, job_id: id, workspace: target.workspace, thread_id: target.threadId, title: target.title, result_delivery: 'background_to_wechat', ...(job.error ? { warning: job.error } : {}) };
  }

  status() {
    const jobId = this.state().latestJobId;
    if (!jobId) return { thread_id: this.settings.targetThreadId, workspace: this.settings.targetWorkspace, status: 'no_task' };
    const job = this.getJob(jobId);
    if (job.threadId !== this.settings.targetThreadId) throw new Error('The saved task belongs to a different target.');
    if (['waiting', 'waiting_desktop', 'running'].includes(job.status)) this.startWatcher(job.id);
    return { job_id: job.id, workspace: job.workspace, thread_id: job.threadId, title: job.title, status: job.status, delivery: job.delivery, error: job.error, result: job.result };
  }
}

export function findJobTurn(turns, job) {
  if (job.turnId) return turns.find(turn => turn.id === job.turnId);
  return turns.find(turn => turn.items?.some(item => {
    if (item.type === 'userMessage') return item.content?.some(part => part.type === 'text' && part.text?.includes(job.marker));
    return item.type === 'functionCallOutput' && item.name === 'send_message_to_thread' && item.namespace === 'codex_app'
      && item.output?.text?.includes(job.marker)
      && item.output.text.includes(`<source_thread_id>${callerThread(job.metadata)}</source_thread_id>`);
  }));
}

export function completedResult(turn) {
  if (!['completed', 'failed', 'interrupted', 'cancelled'].includes(turn.status)) return null;
  const finals = (turn.items ?? []).filter(item => item.type === 'agentMessage' && (!item.phase || item.phase === 'final_answer')).map(item => item.text).filter(Boolean);
  if (finals.length) return `${turn.status === 'completed' ? '' : `本轮状态：${turn.status}。\n\n`}${finals.join('\n\n')}`;
  return turn.error?.message || `会话本轮状态：${turn.status}，没有最终回复。`;
}

export function sendWeChat(settings, message) {
  const body = JSON.stringify({ project: settings.reply.project, session_key: settings.reply.sessionKey, message });
  // Node/libuv on Windows treats socketPath as a named pipe, whereas cc-connect
  // listens on AF_UNIX. Windows curl supports that socket transport directly.
  if (process.platform === 'win32') return sendWindowsSocket(settings, body);
  return new Promise((resolve, reject) => {
    const request = http.request({ socketPath: settings.reply.apiSocket, path: '/send', method: 'POST', headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) }, timeout: 30000 }, response => {
      let text = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { text += chunk; });
      response.on('end', () => {
        if (response.statusCode !== 200) return reject(new Error(`WeChat bridge rejected the result: ${response.statusCode} ${text}`));
        try { if (JSON.parse(text).status !== 'ok') throw new Error('Unexpected send response'); resolve(); }
        catch (error) { reject(error); }
      });
    });
    request.on('timeout', () => request.destroy(new Error('WeChat delivery timed out; not automatically retried')));
    request.on('error', reject);
    request.end(body);
  });
}

export function sendWindowsSocket(settings, body) {
  return new Promise((resolve, reject) => {
    const child = spawn(settings.reply.curlPath || 'curl.exe', ['--silent', '--show-error', '--fail-with-body', '--max-time', '30', '--unix-socket', settings.reply.apiSocket, '-H', 'Content-Type: application/json', '--data-binary', '@-', 'http://localhost/send'], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
    let output = ''; let errorText = '';
    child.stdout.on('data', data => { output += data; });
    child.stderr.on('data', data => { errorText += data; });
    child.on('error', reject);
    child.stdin.on('error', reject);
    child.on('exit', code => {
      if (code !== 0) return reject(new Error(`WeChat delivery was not confirmed: ${code} ${errorText} ${output}`));
      try { if (JSON.parse(output).status !== 'ok') throw new Error('Unexpected send response'); resolve(); }
      catch (error) { reject(error); }
    });
    child.stdin.end(body);
  });
}

export async function deliverTurn(router, job, result, deliver) {
  const key = createHash('sha256').update(`${job.threadId}\0${job.turnId}`).digest('hex');
  const receiptFile = path.join(router.settings.stateDirectory, 'receipts', `${key}.json`);
  fs.mkdirSync(path.dirname(receiptFile), { recursive: true });
  let fd;
  try { fd = fs.openSync(receiptFile, 'wx', 0o600); }
  catch (error) {
    if (error.code !== 'EEXIST') throw error;
    const receipt = readJSON(receiptFile);
    return { delivery: receipt.status === 'sent' ? 'sent_with_other_request' : 'covered_by_other_request', deliveryJobId: receipt.jobId };
  }
  fs.writeFileSync(fd, JSON.stringify({ jobId: job.id, status: 'sending' }));
  fs.closeSync(fd);
  try {
    await deliver(router.settings, `boss，${job.title}的结果：\n\n${result}`);
    writeJSON(receiptFile, { jobId: job.id, status: 'sent' });
    return { delivery: 'sent' };
  } catch (error) {
    writeJSON(receiptFile, { jobId: job.id, status: 'failed_or_unknown', error: error.message });
    return { delivery: 'failed_or_unknown', error: error.message };
  }
}

export async function watchJob(router, id, deliver = sendWeChat) {
  const job = router.getJob(id);
  if (!['waiting', 'waiting_desktop', 'running'].includes(job.status)) return;
  job.status = 'waiting';
  delete job.error;
  try {
    for (;;) {
      let cursor;
      let turn;
      // Search the submitted user message, not merely the latest finished turn.
      // This prevents a busy desktop chat's earlier result reaching WeChat.
      for (let page = 0; page < 20 && !turn; page++) {
        const result = await router.client.call('read_thread', { hostId: job.hostId, threadId: job.threadId, turnLimit: 10, includeOutputs: true, maxOutputCharsPerItem: 20000, ...(cursor ? { cursor } : {}) });
        turn = findJobTurn(result.turns ?? [], job);
        cursor = result.page?.nextCursor;
        if (!cursor) break;
      }
      if (turn) {
        job.turnId = turn.id;
        job.status = 'running';
        const result = completedResult(turn);
        if (result !== null) {
          job.status = turn.status;
          job.result = result;
          job.delivery = 'sending';
          router.saveJob(job);
          Object.assign(job, await deliverTurn(router, job, result, deliver));
          job.completedAt = new Date().toISOString();
          router.saveJob(job);
          return;
        }
      }
      router.saveJob(job);
      const snapshot = await router.client.call('wait_threads', { targets: [{ hostId: job.hostId, threadId: job.threadId, ...(job.cursor ? { afterCursor: job.cursor } : {}) }], timeoutMs: 45000 });
      if (snapshot.errors?.length) throw new Error(JSON.stringify(snapshot.errors));
      const poll = snapshot.polls?.find(item => item.thread?.id === job.threadId);
      if (poll?.cursor) job.cursor = poll.cursor;
      if (snapshot.wake?.reason === 'needsAttention' && job.cursor !== job.lastAttentionCursor) {
        await deliver(router.settings, `boss，${job.title}需要你在 Codex 桌面确认或补充信息，确认后会继续回传结果。`);
        job.lastAttentionCursor = job.cursor;
      }
      router.saveJob(job);
    }
  } catch (error) {
    job.status = 'waiting_desktop';
    job.error = error.message;
    router.saveJob(job);
  }
}
