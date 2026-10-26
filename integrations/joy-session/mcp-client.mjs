import fs from 'node:fs';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { randomUUID } from 'node:crypto';

export function discoverPipe(seed, entries) {
  const match = /^\\\\\.\\pipe\\(codex-app-tools-.*?)[0-9a-f-]{36}$/i.exec(seed ?? '');
  if (!match) throw new Error('Desktop connection must be supplied by CODEX_APP_TOOLS_PIPE_PATH.');
  const candidates = entries.filter(name => name.startsWith(match[1]));
  if (candidates.length !== 1) throw new Error(`Expected one Codex desktop connection, found ${candidates.length}. Open the desktop app and reconnect Joy.`);
  return `\\\\.\\pipe\\${candidates[0]}`;
}

export function desktopPipe(settings) {
  const seed = process.env.CODEX_APP_TOOLS_PIPE_PATH || settings.pipePath;
  if (process.platform !== 'win32') {
    if (!seed) throw new Error('Desktop connection is not configured.');
    return seed;
  }
  return discoverPipe(seed, fs.readdirSync('\\\\.\\pipe\\'));
}

export function officialServer(settings) {
  if (settings.serverPath && fs.existsSync(settings.serverPath)) return settings.serverPath;
  const versions = fs.readdirSync(settings.pluginDirectory).sort((a, b) => b.localeCompare(a, undefined, { numeric: true }));
  for (const version of versions) {
    const candidate = path.join(settings.pluginDirectory, version, 'server.mjs');
    if (fs.existsSync(candidate)) return candidate;
  }
  throw new Error('The installed official codex-app-tools MCP server was not found.');
}

export class DesktopClient {
  constructor(settings, metadata) {
    this.settings = settings;
    this.metadata = metadata;
    this.pending = new Map();
    this.nextId = 1;
  }

  async start() {
    this.child = spawn(this.settings.nodePath || process.execPath, [officialServer(this.settings)], {
      windowsHide: true,
      stdio: ['pipe', 'pipe', 'pipe'],
      env: { ...process.env, CODEX_APP_TOOLS_PIPE_PATH: desktopPipe(this.settings) },
    });
    this.stderr = '';
    this.child.stderr.on('data', data => { this.stderr = (this.stderr + data).slice(-2000); });
    this.child.on('error', error => this.failPending(error));
    this.child.on('exit', code => this.failPending(new Error(`Desktop MCP exited (${code}): ${this.stderr}`)));
    this.lines = createInterface({ input: this.child.stdout });
    this.lines.on('line', line => {
      let message;
      try { message = JSON.parse(line); } catch { return; }
      const request = this.pending.get(message.id);
      if (!request) return;
      this.pending.delete(message.id);
      clearTimeout(request.timer);
      if (message.error) request.reject(new Error(message.error.message));
      else request.resolve(message.result);
    });
    await this.request('initialize', { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'joy-session', version: '1.0.0' } });
    this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' })}\n`);
    const catalog = await this.request('tools/list', {});
    const names = new Set(catalog.tools.map(tool => tool.name));
    for (const name of ['read_thread', 'send_message_to_thread', 'wait_threads']) {
      if (!names.has(name)) throw new Error(`Official desktop tool is unavailable: ${name}`);
    }
    return this;
  }

  request(method, params, timeout = 65000) {
    return new Promise((resolve, reject) => {
      const id = this.nextId++;
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`${method} timed out; not automatically retried`)); }, timeout);
      this.pending.set(id, { resolve, reject, timer });
      this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`, error => {
        if (error) { clearTimeout(timer); this.pending.delete(id); reject(error); }
      });
    });
  }

  async call(name, args) {
    // A workspace operation can make several downstream app calls. Preserve
    // its real caller/turn, but do not reuse the enclosing tool invocation ID.
    const metadata = { ...this.metadata, 'openai/toolCallId': `joy-desktop-${randomUUID()}` };
    const result = await this.request('tools/call', { name, arguments: args, _meta: metadata });
    const text = (result.content ?? []).filter(block => block.type === 'text').map(block => block.text).join('\n');
    if (result.isError) throw new Error(text || `Desktop tool ${name} failed`);
    try { return JSON.parse(text); } catch { return { text }; }
  }

  failPending(error) {
    for (const request of this.pending.values()) { clearTimeout(request.timer); request.reject(error); }
    this.pending.clear();
  }

  async close() {
    if (!this.child || this.child.exitCode !== null) return;
    this.lines?.close();
    this.child.stdin.end();
    await new Promise(resolve => {
      const timer = setTimeout(() => { this.child.kill(); resolve(); }, 1500);
      this.child.once('exit', () => { clearTimeout(timer); resolve(); });
    });
  }
}
