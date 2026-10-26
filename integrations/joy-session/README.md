# Joy: one fixed desktop session

Joy keeps its WeChat conversation and sends work to one configured existing
Codex desktop chat. There is no session picker, workspace router, binding
manager, thread creation, or subagent dispatch.

The local MCP server exposes only:

- `send_task(message)`: submit work or a follow-up to the fixed target.
- `task_status()`: inspect its latest request and WeChat delivery status.

The target ID is configuration, not a tool argument. Every send verifies that
it still belongs to the configured local workspace and is not Joy itself.
The destination keeps its existing model and permissions. Desktop operations
use the installed, unmodified official `codex-app-tools` MCP server.

Run `node server.mjs serve <settings.json>`. Configure it only for Joy through
cc-connect's `cmd` array and per-process `-c mcp_servers.joy_session...` arguments.
`codex app-server` does not support `--profile`. Settings contain:

- `stateDirectory`, `joyThreadId`, `targetThreadId`, `targetWorkspace`;
- `pluginDirectory`, optional `serverPath`/`nodePath`, and the desktop-provided `pipePath`;
- `reply: { apiSocket, project, sessionKey }` for cc-connect's local API.

The desktop app must remain open. On Windows, connection discovery requires
exactly one pipe matching the supplied official prefix. The bridge never speaks
the private pipe protocol directly. Executor-supplied caller identity is kept;
each downstream tool request gets its own invocation ID.

After submission, a hidden watcher matches the request in the destination's
delegation input and returns only the final answer to WeChat. It avoids old
results and duplicate replies when several requests finish in one turn. Joy can
keep chatting or go idle while the watcher runs. Startup/status checks resume
observation after a disconnect without resending the task. Uncertain submissions
or delivery failures are recorded and are not automatically retried.

`state.json`, `jobs/`, and `receipts/` are local durable state. Keep machine-specific
settings and chat history out of Git. No automatic deletion is performed.

Tests: `node --test integrations/joy-session/*.test.mjs`.