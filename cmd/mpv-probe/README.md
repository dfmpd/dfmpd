# M0 mpv IPC probe

Run from the repository root on Unix with Go and mpv installed:

```sh
go run ./cmd/mpv-probe /absolute/path/to/audio.mp3
```

A local file loads immediately. Enter `status`, `pause`, `resume`, `stop`,
`play` (reload from the beginning), or `quit`. EOF, SIGINT and SIGTERM also
close the probe. `-mpv /path/to/mpv` selects the executable.

The probe starts an isolated mpv with user configuration disabled. Its private
socket directory is created under a usable XDG_RUNTIME_DIR, otherwise the
system temporary directory. Startup is bounded to five seconds, each IPC call
to three seconds. Shutdown requests quit, then kills and reaps an unresponsive
child before removing the owned directory.

JSON requests use `command` and increasing `request_id`; responses use
`request_id`, `error` and `data`. Events are consumed separately. An `end-file`
event with `reason=error` is exposed through `playback_error`, using `file_error`
when present. Status queries `idle-active`, `pause` and, while active, `time-pos`.
Load acknowledgement alone does not prove successful decoding or audible output.
Status is a sequence of queries, so it can report a transient error during a
track transition. Inspect status again after loading.

```sh
go test -race ./...
go vet ./...
```

Tests cover IPC responses/events, errors, malformed messages, disconnects,
timeouts, failed startup cleanup, real mpv normal/abnormal exit, and repeated
cleanup. Real mpv tests skip explicitly if mpv is unavailable. Playback tests
use generated PCM WAV and null audio output to verify load, pause, resume and
stop without a sound device. Separately verify an MP3 or FLAC with actual audio
output and check that no child or socket remains after quit or Ctrl-C.

This is a probe, not a daemon or public Source/Playback API. Seek, plugin
loading, client IPC, queues and restart supervision are intentionally deferred.
IPC timeout does not cancel an already delivered mpv operation. Parent SIGKILL
or machine failure cannot run cleanup; orphan prevention for that case is not
implemented. Playback failure is observed with `status`, not a frontend event
subscription. The stdin-reading goroutine may remain blocked until the command
process exits; it does not own child or socket resources.
