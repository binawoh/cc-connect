import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { SessionBridge, callerThread, findJobTurn, watchJob, deliverTurn, readJSON } from './router.mjs';
import { discoverPipe } from './mcp-client.mjs';

function fixture(t) {
  const root = fs.realpathSync.native(os.tmpdir());
  const directory = fs.mkdtempSync(path.join(root, 'joy-workspaces-test-'));
  t.after(() => {
    const resolved = fs.realpathSync.native(directory);
    assert.equal(path.dirname(resolved), root);
    assert.ok(path.basename(resolved).startsWith('joy-workspaces-test-'));
    fs.rmSync(resolved, { recursive: true, force: true });
  });
  const a = path.join(directory, 'a'); const b = path.join(directory, 'b');
  fs.mkdirSync(a); fs.mkdirSync(b);
  const settings = { stateDirectory: path.join(directory, 'state'), targetWorkspace: a, targetThreadId: 'session-a', joyThreadId: 'joy-main' };
  const metadata = { 'openai/threadId': 'joy-main' };
  const calls = []; const watches = [];
  const client = { async call(name, args) {
    calls.push({ name, args });
    if (name === 'read_thread') return { thread: { id: args.threadId, kind: 'codex', hostId: 'local', cwd: args.threadId === 'session-b' ? b : a, title: args.threadId } };
    if (name === 'send_message_to_thread') return { status: 'queued' };
    throw new Error(`Unexpected call ${name}`);
  } };
  const router = new SessionBridge(settings, client, metadata, id => watches.push(id));
  return { a, b, settings, metadata, client, calls, watches, router };
}

test('every task and follow-up uses the same configured existing session across restarts', async t => {
  const f = fixture(t);
  await f.router.dispatch('first');
  await f.router.dispatch('second');
  const resumed = new SessionBridge(f.settings, f.client, f.metadata, id => f.watches.push(id));
  await resumed.dispatch('continue the original work');
  const sends = f.calls.filter(call => call.name === 'send_message_to_thread');
  assert.deepEqual(sends.map(call => call.args.threadId), ['session-a', 'session-a', 'session-a']);
  for (const send of sends) assert.deepEqual(Object.keys(send.args).sort(), ['hostId', 'prompt', 'threadId']);
  assert.equal(f.watches.length, 3);
  assert.equal(resumed.status().thread_id, 'session-a');
});

test('wrong workspace and main-session loops never send', async t => {
  const { router, settings, calls } = fixture(t);
  settings.targetThreadId = 'session-b';
  await assert.rejects(router.dispatch('test'), /no longer matches/);
  settings.targetThreadId = 'joy-main';
  await assert.rejects(router.dispatch('test'), /own main session/);
  assert.equal(calls.filter(call => call.name === 'send_message_to_thread').length, 0);
});

test('a fixed session that moved out of its configured workspace is not silently reused', async t => {
  const f = fixture(t);
  await f.router.dispatch('first');
  f.client.call = async () => ({ thread: { id: 'session-a', kind: 'codex', hostId: 'local', cwd: f.b, title: 'moved' } });
  await assert.rejects(f.router.dispatch('continue'), /no longer matches/);
  assert.equal(f.watches.length, 1);
});

test('only the actual Joy caller may dispatch', async t => {
  const f = fixture(t);
  const other = new SessionBridge(f.settings, f.client, { 'openai/threadId': 'different-chat' });
  await assert.rejects(other.dispatch('test'), /Only the configured Joy/);
  assert.equal(callerThread({ 'x-codex-turn-metadata': JSON.stringify({ thread_id: 'joy-main' }) }), 'joy-main');
});

test('uncertain submissions are persisted and never automatically resent', async t => {
  const f = fixture(t);
  const original = f.client.call;
  f.client.call = async (name, args) => {
    if (name === 'send_message_to_thread') throw new Error('connection lost after submit');
    return original(name, args);
  };
  await assert.rejects(f.router.dispatch('test'), /Do not resend automatically/);
  const jobs = fs.readdirSync(path.join(f.settings.stateDirectory, 'jobs'));
  assert.equal(jobs.length, 1);
  const job = readJSON(path.join(f.settings.stateDirectory, 'jobs', jobs[0]));
  assert.equal(job.status, 'send_unknown');
  assert.equal(f.router.status().status, 'send_unknown');
  assert.equal(f.router.status().job_id, job.id);
  assert.equal(f.watches.length, 0);
});

test('watcher forwards only its matching request, including after reconnect', async t => {
  const f = fixture(t);
  const id = randomUUID();
  const job = { id, workspace: f.a, title: 'session-a', threadId: 'session-a', hostId: 'local', metadata: f.metadata, marker: `[Joy request ${id}]`, status: 'waiting_desktop', delivery: 'not_sent', error: 'old connection error' };
  f.router.saveJob(job);
  let reads = 0; const delivered = [];
  const unrelated = { id: 'old-turn', status: 'completed', items: [{ type: 'agentMessage', phase: 'final_answer', text: 'old result' }] };
  const target = { id: 'target-turn', status: 'completed', items: [
    { type: 'functionCallOutput', name: 'send_message_to_thread', namespace: 'codex_app', output: { text: `<codex_delegation><source_thread_id>joy-main</source_thread_id><input>${job.marker}\noriginal question</input></codex_delegation>` } },
    { type: 'agentMessage', phase: 'commentary', text: 'working' },
    { type: 'agentMessage', phase: 'final_answer', text: 'correct result' },
  ] };
  f.client.call = async (name, args) => {
    if (name === 'read_thread') {
      assert.ok(args.maxOutputCharsPerItem <= 20000, 'official read_thread output cap is 20000');
      assert.equal(args.includeOutputs, true, 'delegated input is stored as a functionCallOutput');
      return { turns: ++reads === 1 ? [unrelated] : [unrelated, target], page: {} };
    }
    assert.equal(name, 'wait_threads');
    assert.equal(args.targets[0].threadId, 'session-a');
    return { polls: [{ thread: { id: 'session-a' }, cursor: 'new-cursor' }] };
  };
  await watchJob(f.router, id, async (_settings, text) => delivered.push(text));
  assert.deepEqual(delivered, ['boss，session-a的结果：\n\ncorrect result']);
  assert.equal(f.router.getJob(id).turnId, 'target-turn');
  assert.equal(f.router.getJob(id).delivery, 'sent');
  assert.equal(f.router.getJob(id).error, undefined);
  assert.equal(findJobTurn([unrelated], job), undefined);
});

test('a known turn id takes precedence over a later quote of the request marker', () => {
  const job = { turnId: 'original', marker: '[Joy request original]' };
  const quoted = { id: 'later', items: [{ type: 'userMessage', content: [{ type: 'text', text: job.marker }] }] };
  assert.equal(findJobTurn([quoted, { id: 'original' }], job)?.id, 'original');
});

test('two requests completed in the same desktop turn send only one WeChat result', async t => {
  const f = fixture(t); let deliveries = 0;
  const job = { id: randomUUID(), threadId: 'session-a', turnId: 'shared-turn', title: 'chat' };
  assert.equal((await deliverTurn(f.router, job, 'result', async () => { deliveries++; })).delivery, 'sent');
  assert.equal((await deliverTurn(f.router, { ...job, id: randomUUID() }, 'result', async () => { deliveries++; })).delivery, 'sent_with_other_request');
  assert.equal(deliveries, 1);
});

test('WeChat rejection remains visible and is not retried on observation', async t => {
  const f = fixture(t); let deliveries = 0;
  const job = { id: randomUUID(), threadId: 'session-a', turnId: 'rejected-turn', title: 'chat' };
  const result = await deliverTurn(f.router, job, 'result', async () => { deliveries++; throw new Error('API rejected'); });
  assert.equal(result.delivery, 'failed_or_unknown');
  await deliverTurn(f.router, job, 'result', async () => { deliveries++; });
  assert.equal(deliveries, 1);
});

test('desktop restart discovers one replacement pipe and rejects ambiguity', () => {
  const old = '\\\\.\\pipe\\codex-app-tools-11111111-1111-1111-1111-111111111111';
  const replacement = 'codex-app-tools-22222222-2222-2222-2222-222222222222';
  assert.equal(discoverPipe(old, [replacement, 'unrelated']), `\\\\.\\pipe\\${replacement}`);
  assert.throws(() => discoverPipe(old, []), /found 0/);
  assert.throws(() => discoverPipe(old, [replacement, 'codex-app-tools-33333333-3333-3333-3333-333333333333']), /found 2/);
  assert.throws(() => discoverPipe('not-a-desktop-connection', [replacement]), /must be supplied/);
});
