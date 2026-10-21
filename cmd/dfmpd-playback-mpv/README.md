# External Playback PoC — milestone A

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
- Normal shutdown: 3 seconds, then kill the direct plugin child and wait up to
  one further second. Close is idempotent. OS-level process creation/reaping or
  filesystem stalls are not a hard real-time guarantee.
- Calls are serialized; response IDs are checked. Late responses from timed-out
  requests are discarded, never used as a later request's result. The plugin
  executes requests sequentially, checking each deadline before execution.
- Cancellation of a caller after transmission does not roll back the operation.
  There is no cancel RPC in A; the receiver enforces the transmitted deadline.
  A lost reply, cancellation or timeout can leave the outcome unknown. Core's
  existing unknown-outcome handling is preserved, and mutations are never retried.
- A partial/interrupted write or invalid response invalidates the session rather
  than attempting to reconstruct/replay the request. EOF or plugin failure is
  reported as unavailable. Queue state stays in the daemon.
- The provided plugin terminates the session on an operation deadline/cancellation
  error, with a bounded attempt at backend cleanup; an uncooperative adapter is
  not reused. A late response from another compatible plugin is still handled
  safely by the proxy. No automatic restart or state restoration is attempted.
- Local files must be accessible in the plugin's filesystem namespace with its
  inherited user privileges. URLs, authentication, stream delivery and Source
  process management remain deferred.

## Verification and remaining work

Tests exercise real child processes for initialization, capabilities, all six
operations, invalid frames/IDs, process exit, late replies/no replay, shutdown
limits, and preservation of the Core queue after a crash. Daemon integration
runs both built-in and external paths with two generated FLAC files, verifying
Next/Previous against mpv's loaded path, pause/resume/seek/stop, process exit and
socket cleanup. Real tests skip when mpv/ffmpeg are absent; audio uses null
output, so audible acceptance is still manual.

A guarantees normal cooperative cleanup and bounded waiting in the host.
It does not implement B's descendant-process supervision: a killed/crashed or
uncooperative plugin may leave mpv and temporary files behind. Parent SIGKILL,
process groups/cgroups, full crash recovery, log management and stronger process
isolation remain follow-up work. There is no auto-restart, hot reload, SDK,
repository split, external Source, or generic Plugin Manager.
