# mpv playback probe (M0/M1)

Run from the repository root on Unix with Go and mpv installed:

```sh
go run ./cmd/mpv-probe /absolute/path/to/audio.mp3
```

A local file loads immediately. Enter `status`, `pause`, `resume`, `stop`,
`play` (reload from the beginning), `seek SECONDS` (absolute position), or `quit`. EOF, SIGINT and SIGTERM also
close the probe. `-mpv /path/to/mpv` selects the executable.

The probe starts an isolated mpv with user configuration disabled. Its private
socket directory is created under a usable XDG_RUNTIME_DIR, otherwise the
system temporary directory. Startup is bounded to five seconds, each IPC call
to three seconds. Shutdown requests quit, then kills and reaps an unresponsive
child before removing the owned directory.

JSON requests use `command` and increasing `request_id`; responses use
`request_id`, `error` and `data`. Events are consumed separately. An `end-file`
event with `reason=error` becomes a `media_failed` failure, using `file_error`
when present. Status queries `idle-active`, `pause` and, while active, `time-pos`.
Load acknowledgement alone does not prove successful decoding or audible output.
Status is a sequence of observations, not an atomic snapshot. Unavailable
position during loading is represented by an omitted position. Inspect status
again after loading. The adapter reports backend-independent `state`
(`idle`, `loading`, `playing`, `paused`, `failed`), optional `position_seconds`,
and optional `failure` with `code`/`message`. Idle and pause-intent flags remain
available independently (pause intent may be set while idle). The CLI preserves
the M0 `idle`, `paused`, `position_seconds` and `playback_error` output fields.

```sh
go test -race ./...
go vet ./...
```

Tests cover IPC responses/events, errors, malformed messages, disconnects,
timeouts, failed startup cleanup, real mpv normal/abnormal exit, and repeated
cleanup. Real mpv tests skip explicitly if mpv is unavailable. Playback tests
use generated PCM WAV and null audio output to verify load, pause, resume, absolute seek and
stop without a sound device. Separately verify an MP3 or FLAC with actual audio
output and check that no child or socket remains after quit or Ctrl-C.

This is a probe, not a daemon or public Source/Playback API. Plugin loading,
client IPC, queues and restart supervision are intentionally deferred.
IPC timeout does not cancel an already delivered mpv operation. Parent SIGKILL
or machine failure cannot run cleanup; orphan prevention for that case is not
implemented. Playback failure is observed with `status`, not a frontend event
subscription. The stdin-reading goroutine may remain blocked until the command
process exits; it does not own child or socket resources.

The internal `playback.Adapter` contract uses provisional `Input{Location}` for
local files. It is not the final Media Identity/Descriptor or plugin wire schema.
Play replaces the file and clears pause. Seek uses finite, nonnegative absolute
seconds, translated to mpv's `absolute+exact` mode; actual position can differ
because of media/output buffering. See the [mpv command manual](https://mpv.io/manual/stable/#command-interface).

Adapter calls are serialized with a three-second budget including queueing.
Contexts are local cancellation/deadline controls, not serialized payloads.
Input, Status and structured Error values can be represented as JSON; the
interface exposes no mpv response, socket, process handle or channel. The
concrete mpv adapter retains a diagnostic Done channel for this local probe.
Construction and shutdown supervision remain the host's responsibility.
A future RPC proxy must implement cancellation and lifecycle explicitly.

Errors distinguish invalid input, rejected commands, unavailable backend,
timeout, cancellation and closed adapter. Asynchronous media failure is in
Status. Failed media can be replaced with Play; failed transport requires closing
and reopening the adapter. Stop does not erase the last media failure; Play
starts a new attempt. The M0 transport and tests remain available internally.
