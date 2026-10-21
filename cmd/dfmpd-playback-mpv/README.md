# External Playback — milestones A and B

`dfmpd-playback-mpv` hosts the existing mpv Adapter in a separate process.
Core, Queue, Local Source and client IPC remain unchanged. This is an
experimental same-repository protocol, not a published SDK or plugin manager.

## Run

From the repository root, with Go and mpv installed:

```sh
go build -o /tmp/dfmpd-playback-mpv ./cmd/dfmpd-playback-mpv
go build -o /tmp/dfmpd ./cmd/dfmpd
/tmp/dfmpd -playback-plugin /tmp/dfmpd-playback-mpv
```

External Playback supervision currently requires Linux; other platforms reject
external startup explicitly. The built-in Adapter is unchanged.
The usual private `XDG_RUNTIME_DIR` is required by the daemon. The plugin
executable must be an explicit absolute or relative path containing `/`;
there is no PATH discovery, manifest or installation mechanism. `-mpv` selects
the mpv executable and is forwarded to the plugin as `-mpv executable`.
Without `-playback-plugin`, the built-in Adapter remains available. Failure of
an explicitly selected plugin never falls back to the built-in Adapter.

In another terminal, use the existing client:

```sh
go run ./cmd/dfmpc queue add /absolute/path/first.flac
go run ./cmd/dfmpc queue add /absolute/path/second.flac
go run ./cmd/dfmpc queue list
go run ./cmd/dfmpc queue play 1
go run ./cmd/dfmpc next
go run ./cmd/dfmpc previous
go run ./cmd/dfmpc pause
go run ./cmd/dfmpc resume
go run ./cmd/dfmpc seek 10
go run ./cmd/dfmpc status
go run ./cmd/dfmpc stop
```

Use the actual entry IDs returned by add/list. Stop the daemon with SIGINT or
SIGTERM. The plugin shuts down mpv before acknowledging `plugin.shutdown`.
Do not launch the plugin as an interactive CLI: stdin/stdout are protocol-only;
stderr carries diagnostics. mpv stdout is not forwarded to the protocol stream.

## Private protocol, experimental version 1

Transport is the child's stdin/stdout, newline-delimited UTF-8 JSON, maximum
64 KiB per frame including LF. This uses JSON-RPC 2.0 request/response envelopes
but is a restricted profile: no batches, notifications or events. Malformed
frames terminate the session. It is distinct from both public DFMPD client IPC
and mpv JSON IPC; `internal/ipc` is not reused.

Every request has a unique increasing decimal string `id`, object `params`,
and a required `deadline_unix_ms` extension. The deadline is an absolute Unix
millisecond timestamp for this same-host PoC, rounded up by the sender. The
plugin caps the remaining budget and does not start already-expired requests.
This is not a remote clock synchronization or stable external-plugin contract.
Responses echo the ID and contain exactly one of `result` and `error`.

| Method | Params | Successful result |
|---|---|---|
| `plugin.initialize` | `{"protocol_version":1}` | capabilities below |
| `plugin.shutdown` | `{}` | `{"accepted":true}` after backend cleanup |
| `playback.play` | `{"location":"/absolute/path/song.flac"}` | `{"accepted":true}` |
| `playback.pause` | `{}` | `{"accepted":true}` |
| `playback.resume` | `{}` | `{"accepted":true}` |
| `playback.stop` | `{}` | `{"accepted":true}` |
| `playback.seek` | `{"seconds":10}` | `{"accepted":true}` |
| `playback.status` | `{}` | normalized Playback Status |

Initialization precedes all Playback requests. The daemon checks protocol
version, kind, nonempty implementation name, all six operations, and local-file
input support. Current capabilities:

```json
{"protocol_version":1,"kind":"playback","name":"dfmpd-playback-mpv","operations":["play","pause","resume","stop","seek","status"],"inputs":["local-file"]}
```

Status uses `state`, `idle`, `paused`, optional `position_seconds` and optional
`failure` with `code`/`message`. Operation errors use JSON-RPC code `-32000` and
`data: {"code":"...","message":"..."}` with the existing portable Playback
error codes. Go contexts, process handles and raw mpv responses do not cross
the boundary. `accepted` still means command acceptance, not audible playback.

## Deadlines and failure semantics

- Startup/initialization: at most 5 seconds of protocol waiting, followed by
  bounded cleanup on failure. RPC: at most 3 seconds, shortened by caller context.
- Calls are serialized and IDs checked. Canceling while still waiting for the
  call slot does not invalidate a healthy session. Once transmission begins,
  cancellation/timeout invalidates the session and starts automatic cleanup.
  Late or already-buffered replies cannot revive it or satisfy another request.
- The triggering timeout/cancellation returns its context error; subsequent
  operations report unavailable. Malformed responses, EOF, process exit and
  backend unavailable/closed errors also retire the session. There is no retry,
  automatic restart, state restoration or fallback to the built-in Adapter.
- A timeout, cancellation or lost reply does not prove the mutation failed to
  execute. Terminating the process group may stop audio, but is not a successful
  Playback Stop acknowledgement. Core retains its queue and existing uncertainty
  semantics. A previously accepted selection is the last acknowledged command,
  not evidence that the backend is still alive; playback status returns an error.
- Normal Close gives `plugin.shutdown` up to 3 seconds. Failure starts group
  cleanup: SIGTERM, up to 1 second for the leader to exit, then SIGKILL to the
  group, including leftover descendants even if the leader has already exited.
  Exit observation, direct-child Wait and stream draining each have a 1-second
  fallback bound. Repeated or concurrent Close shares the same cleanup result.
  Automatic cleanup starts on failure even when nobody calls Close.
- Normal shutdown still lets the plugin close/reap mpv before acknowledgement.
  Linux `waitid(WNOWAIT)` observes plugin death independently of pipe EOF and
  reserves the leader PID until the final group signal. Only then is the direct
  child reaped with `cmd.Wait`. No group signal follows reaping, avoiding PGID
  reuse races. A grandchild holding stdout open cannot hide the leader's death.
- Local files must be accessible in the plugin's filesystem namespace with its
  inherited user privileges. URLs, authentication and Source process management
  remain deferred. The protocol version and public client IPC are unchanged.

## Guarantees and OS limits

Plugins and normally inherited descendants run in a dedicated process group.
While the daemon is alive and permitted to signal that group, abnormal plugin
exit, stalled RPC, invalid responses and shutdown timeouts trigger group
termination without blocking Core indefinitely. The daemon itself is not in
that group. No process-wide subreaper or wildcard child reaping is installed.

The daemon reaps its direct plugin child. During normal shutdown the plugin
reaps mpv. If the plugin dies first, orphaned descendants are adopted and reaped
by init/systemd (or the environment's subreaper); the daemon can terminate them
but cannot `Wait` for processes that are not its children. A broken container
PID 1 may leave zombies even after the audio process has terminated.

SIGKILL of the **daemon**, machine failure, a descendant escaping the group via
`setsid`/`setpgid`, or changed signal permissions is outside this guarantee.
Processes in uninterruptible kernel sleep may not exit promptly even on SIGKILL;
cleanup reports a timeout and retains a background direct-child reaper instead
of hanging shutdown. OS process creation/signaling/filesystem stalls are not a
hard real-time guarantee. Use service/cgroup supervision for stronger containment.

An abruptly killed plugin may leave its private mpv socket/directory behind.
The daemon does not scan or delete unknown runtime files. This is not a general
plugin sandbox or crash-recovery manager; automatic restart, hot reload, SDK,
repository split and external Source remain unimplemented.

## Verification

Tests use real child/grandchild processes and control-socket handshakes to
establish readiness and request receipt. They cover SIGKILL, idle crash with
inherited stdout held open, EOF, invalid responses, missing responses, late
buffered replies/no replay, concurrent Close, and TERM-resistant descendants.
The test process temporarily acts as a subreaper to assert and reap orphaned
children without depending on the host/container PID 1; production does not.

The existing daemon tests still run built-in and external paths with two FLAC
files, queue navigation, pause/resume/seek/stop and normal socket cleanup.
Additional real-mpv tests kill or SIGSTOP the plugin during WAV/null-output
playback, verify mpv termination, and verify that daemon.info and queue.list
continue to work while failed Stop/Status/Play remain errors. Real audio tests
skip when required tools are absent. Automated audio output is null; the
operator separately confirmed MP3 playback and CLI commands after A.
