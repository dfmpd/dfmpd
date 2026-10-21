// dfmpd hosts one foreground playback session and its local client IPC.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/ipc"
	"github.com/dfmpd/dfmpd/internal/playback/mpv"
	"github.com/dfmpd/dfmpd/internal/source"
	"github.com/dfmpd/dfmpd/internal/source/local"
)

func main() {
	executable := flag.String("mpv", "mpv", "mpv executable")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: dfmpd [-mpv executable]")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *executable); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, executable string) (err error) {
	socket, err := ipc.Listen(os.Getenv("XDG_RUNTIME_DIR"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, socket.Close()) }()
	adapter, err := mpv.Open(ctx, executable)
	if err != nil {
		return err
	}
	session := core.NewWithSources(ctx, adapter, map[string]source.Resolver{local.ID: local.Resolver{}})
	defer func() { err = errors.Join(err, session.Close()) }()
	fmt.Fprintln(os.Stderr, "dfmpd listening on", socket.Path())
	return ipc.Serve(ctx, socket.UnixListener, session)
}
