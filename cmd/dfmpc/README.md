# dfmpc

Independent CLI for the existing DFMPD Unix socket. It never starts or manages
the daemon. Run `go run ./cmd/dfmpd` in another terminal first.

From the repository root:

```sh
go run ./cmd/dfmpc info
go run ./cmd/dfmpc queue add "./first song.flac"
go run ./cmd/dfmpc queue add "./second song.flac"
go run ./cmd/dfmpc queue list --offset 0 --limit 20
go run ./cmd/dfmpc queue play 1
go run ./cmd/dfmpc next
go run ./cmd/dfmpc previous
go run ./cmd/dfmpc pause
go run ./cmd/dfmpc resume
go run ./cmd/dfmpc seek 10
go run ./cmd/dfmpc status
go run ./cmd/dfmpc stop
go run ./cmd/dfmpc queue remove 2
```

Use the entry IDs returned by add/list. Relative paths are converted to absolute
paths in the CLI; queue add does not validate file availability until playback.
Repeated adds are independent queue entries. Next/previous work only while a
queue item is playing; stop preserves selection, and `queue play ID` restarts it.
There is no automatic advance or persistence.

`play FILE` invokes the existing M2 direct playback API and clears selection on
success, while retaining the queue. Selected/uncertain attempted entries cannot
be removed; select another item or use direct playback first.

Default socket: `$XDG_RUNTIME_DIR/dfmpd/control.sock`.
Global options precede the command:

```sh
go run ./cmd/dfmpc --socket /path/to/control.sock --json queue list
```

Output is JSON (indented by default, compact with `--json`); errors go to stderr
and produce a nonzero exit status. Each invocation makes one connection and
request, with an eight-second total timeout and no automatic retry.
A timeout or lost response does not prove the operation was not executed.
Check queue outcome/selection and playback status before issuing another command.
For pagination, follow `next_offset`; the actual page may be smaller than the
requested limit to respect the message-size limit.

See [daemon protocol documentation](../dfmpd/README.md) for wire methods,
limits and uncertainty semantics.
