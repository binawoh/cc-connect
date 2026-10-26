import test from 'node:test';
import assert from 'node:assert/strict';
import { DesktopClient } from './mcp-client.mjs';

test('nested desktop requests retain the caller but never reuse the enclosing MCP call id', async () => {
  const source = { threadId: 'actual-joy-thread', callId: 'enclosing-workspace-call', 'x-codex-turn-metadata': { thread_id: 'actual-joy-thread', turn_id: 'actual-turn' } };
  const client = new DesktopClient({}, source);
  const received = [];
  client.request = async (_method, params) => {
    received.push(params);
    return { content: [{ type: 'text', text: '{}' }] };
  };
  await client.call('read_thread', { threadId: 'target' });
  await client.call('send_message_to_thread', { threadId: 'target', prompt: 'test' });
  const ids = received.map(item => item._meta['openai/toolCallId'] || item._meta.callId);
  assert.notEqual(ids[0], ids[1]);
  assert.ok(ids.every(id => id && id !== source.callId));
  assert.ok(received.every(item => item._meta.threadId === source.threadId));
  assert.deepEqual(received[0]._meta['x-codex-turn-metadata'], source['x-codex-turn-metadata']);
});
