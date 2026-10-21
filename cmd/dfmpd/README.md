# DFMPD daemon (M2–M3)

`dfmpd` is a foreground daemon with one shared Playback Adapter. It neither
forks into the background nor installs/manages a service. Future systemd user
service integration is separate work. M3 adds the independent `dfmpc` client,
a Core-owned queue and a Local Source Resolver. Plugin management and event
subscriptions are not implemented.

## Start and stop

From the repository root, with Go and mpv installed:

```sh
go run ./cmd/dfmpd
```

For direct signal handling, build a binary and run it:

```sh
go build -o /tmp/dfmpd ./cmd/dfmpd
/tmp/dfmpd
```

`-mpv /path/to/mpv` selects the executable. `XDG_RUNTIME_DIR` must be an absolute
path to an existing, real directory owned by the current user with mode `0700`.
The daemon creates `$XDG_RUNTIME_DIR/dfmpd` with mode `0700` and listens at
`$XDG_RUNTIME_DIR/dfmpd/control.sock` with mode `0600`. Existing directories must
also have the expected ownership/mode. There is no automatic shared `/tmp`
fallback. Tests use private temporary runtime directories.

SIGINT/Ctrl-C or SIGTERM stops accepting connections, cancels requests, closes
clients and closes the adapter to terminate/reap mpv. The daemon then removes
only the socket inode it created; the empty `dfmpd` directory remains. Partial
startup failures also clean up owned resources. Existing files, symlinks and
sockets are never deleted to make startup succeed. A second instance fails.
After a crash, verify that no daemon owns the socket before manually removing a
stale socket. Same-user processes are trusted; this is not a sandbox against
malicious programs running as the same user.

## Client protocol, API version 1

This is DFMPD client IPC, **not mpv JSON IPC** or an external-plugin protocol.
Transport: Unix stream socket, UTF-8, one JSON object terminated by LF per frame.
A final JSON object without LF is incomplete and is not executed. JSON strings
must escape embedded newlines. Multiple frames may share a connection.

M2 implements a **restricted JSON-RPC 2.0 profile, not full compliance**:

- Requests require `jsonrpc: "2.0"`, a string method and an optional ID. String,
  numeric and null IDs are echoed without converting numbers through float64;
  string IDs are recommended. IDs correlate replies, not deduplicate commands.
- Valid requests without IDs are notifications: execute without a response,
  including when an operation fails. This does not provide event subscriptions.
- Batches, including empty arrays and notification-only batches, return one
  `-32600` error with `id: null`. **No batch element is executed.**
- Methods accept named/object parameters only; positional arrays are rejected
  with `-32602`. Parameters are case-sensitive; unknown fields are rejected.
- Standard errors: `-32700` malformed JSON, `-32600` invalid request/unsupported
  batch, `-32601` unknown method, `-32602` invalid parameters, `-32603` internal
  error (including an oversized response).

| Method | Parameters | Result |
|---|---|---|
| `daemon.info` | omitted or `{}` | `{"api_version":1,"batch":false,"queue":true}` |
| `playback.play` | `{"location":"/absolute/path/audio.flac"}` | `{"accepted":true}` |
| `playback.pause` | omitted or `{}` | `{"accepted":true}` |
| `playback.resume` | omitted or `{}` | `{"accepted":true}` |
| `playback.stop` | omitted or `{}` | `{"accepted":true}` |
| `playback.seek` | `{"seconds":30.5}` | `{"accepted":true}` |
| `playback.status` | omitted or `{}` | observed playback state |

`location` is a provisional local-file test seam, not the final Source/Media
Identity/Descriptor contract. Seek is finite, nonnegative, absolute seconds.
Successful commands mean acceptance, not proof of audible playback. Status
queries the adapter each time: there is no polling, state cache or push.
Status includes `state` (`idle/loading/playing/paused/failed`), `idle`, `paused`,
optional `position_seconds` and optional media `failure` with `kind`/`message`.
Pause intent may be set while idle. The snapshot consists of observations and
is not atomic with respect to mpv's asynchronous playback transitions.

Operation errors use `error.data.kind`: `-32001` timed_out, `-32002` canceled,
`-32003` unavailable, `-32004` closed, `-32005` rejected, or `-32000` other
playback failures. Invalid local playback input uses `-32602`. An asynchronous
media failure is a successful status response with `state: "failed"`; transport
failure is an RPC error. mpv death does not terminate the daemon: `daemon.info`
remains available, but no automatic backend restart is attempted.

## Test requests

Use a socket tool such as `socat` if installed (it is not a runtime dependency):

```sh
socat - UNIX-CONNECT:"$XDG_RUNTIME_DIR/dfmpd/control.sock"
```

Enter one request per line, replacing the absolute audio path:

```json
{"jsonrpc":"2.0","id":1,"method":"daemon.info"}
{"jsonrpc":"2.0","id":2,"method":"playback.play","params":{"location":"/absolute/path/audio.flac"}}
{"jsonrpc":"2.0","id":3,"method":"playback.status"}
{"jsonrpc":"2.0","id":4,"method":"playback.pause"}
{"jsonrpc":"2.0","id":5,"method":"playback.seek","params":{"seconds":10}}
{"jsonrpc":"2.0","id":6,"method":"playback.resume"}
{"jsonrpc":"2.0","id":7,"method":"playback.stop"}
```

Closing this client leaves playback running. All clients share one Core-owned
adapter. Operations execute serially in Core acceptance order; simultaneous
connections have no guaranteed precedence. Responses are written outside Core,
so a client that does not read cannot hold the playback execution slot.

## Limits and failure semantics

- Maximum request/response frame: 64 KiB including LF. Oversized requests receive
  an error when possible and their connection closes without executing them.
  An oversized response becomes an error; if even the error with its echoed ID
  exceeds the limit, the connection closes without a response.
- Maximum accepted concurrent connections: 16. Excess connections close without
  a response. Each connection executes at most one request at a time.
- Frame read deadline: 30 seconds, including idle time; write deadline: 3 seconds.
- Core request deadline: 3 seconds including waiting for the playback slot.
  Expired waiting requests do not reach the adapter.
- Disconnecting does not cancel an admitted request. Its daemon-side deadline
  remains active. A lost response or timeout does not prove the operation failed
  to execute; reconnect and query status before deciding whether to retry.
- The inherited M1 local-file `os.Stat` cannot be interrupted by context, and
  Adapter.Close has no deadline. An OS/filesystem stall can hold the Core worker
  and delay shutdown even though RPC callers receive timeouts. M2 does not alter
  M1 to claim a hard process-exit deadline.
- SIGKILL or machine failure cannot run cleanup and may leave mpv/socket remnants.
  No persistence, automatic recovery or external-plugin lifecycle is implemented.
- The runtime ownership checks currently target Linux/Unix; Windows named pipes
  and cross-platform credential handling are outside M2.

## Validation

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Core tests use a fake adapter; IPC tests cover protocol errors, complete batch
rejection, framing, multiple clients, disconnects, deadlines, connection/message
limits and filesystem ownership. Subprocess tests run the actual daemon
entrypoint with real mpv and generated WAV/null audio output, covering commands,
client reconnection, mpv crash, SIGINT/SIGTERM, child reaping and socket cleanup.
Real mpv tests explicitly skip if mpv is not installed. M0/M1 tests remain intact.
Audible FLAC playback over this new IPC path still needs manual verification.

## M3 queue methods

All queue operations share Core's playback serialization and deadline.
The daemon registers only the Local Source; Core treats item IDs as opaque.
The CLI converts relative paths to absolute paths before sending requests.

| Method | Parameters |
|---|---|
| `queue.add` | `{"identity":{"source_id":"local","item_id":"/absolute/path/song.flac"}}` |
| `queue.list` | `{"offset":0,"limit":20}` (both optional) |
| `queue.remove` | `{"entry_id":"1"}` |
| `queue.play` | `{"entry_id":"1"}` |
| `queue.next` / `queue.previous` | omitted or `{}` |

Results contain `entries`, `total`, `offset`, `next_offset`, `revision`,
`outcome`, and optional `selected_entry_id` / `attempted_entry_id`.
Each entry contains `entry_id` and `identity` only. Repeated identities receive
different entry IDs. IDs are valid only for this daemon lifetime.
Add returns the added entry; other mutation results contain no entry list.
Use `queue.list` to retrieve entries.

List defaults to offset 0 and limit 20; limit must be 1–100. Follow
`next_offset` until null. The server can return fewer entries to keep the entire
encoded response, including LF, within 64 KiB. Pages are individually serialized,
not a transaction across requests; restart pagination if revisions differ.
Capacity is 1024 entries, source IDs are at most 64 UTF-8 bytes, item IDs at most
2048 bytes, and queue request IDs at most 1024 encoded JSON bytes.
Invalid inputs use -32602; queue errors use -32010 and `error.data.kind`.

Resolve happens immediately before every queue play. A resolution failure leaves
playback and the queue unchanged. Descriptors, delivery URLs and credentials are
never included in queue results. Local identities themselves contain paths,
which remain visible to clients; renaming a file invalidates that identity.
Local metadata currently supplies only a filename title, without tag extraction.

An `accepted` outcome and selection identify the last acknowledged queue play,
not successful decoding or audible output. Once an adapter play is attempted,
a failure/timeout may represent partial execution: outcome remains `unknown`,
with the attempted entry ID when applicable. Query both queue and playback
state; playback status alone cannot identify the loaded track. Explicit successful
play or stop resolves the uncertainty. The client does not automatically retry.

Next/previous require an accepted, currently playing queue selection. Paused,
stopped, unknown outcomes and queue boundaries return errors without changing
playback. There is no wraparound or natural-end auto advance. Stop retains the
selection; use `queue.play` to restart it. Selected or uncertain attempted entries
cannot be removed; first select another entry or successfully use direct play.
Successful legacy `playback.play` clears the queue selection but preserves entries.
M2 playback request/result formats remain unchanged.

No queue persistence, media discovery, remote Source, metadata publication,
external-plugin runtime or Player integrations are implemented.
M3 tests additionally cover concurrent queue clients, duplicate identities,
bounded pagination, resolution failure and uncertain playback outcomes.
With mpv and ffmpeg installed, two generated FLAC files exercise queue navigation
and verify mpv's loaded path using null audio output; this is not audible testing.
