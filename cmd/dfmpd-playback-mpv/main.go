// dfmpd-playback-mpv is an experimental stdio Playback plugin host.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	sdk "github.com/dfmpd/dfmpd-sdk/playback"
	"github.com/dfmpd/dfmpd/internal/playback/mpv"
	"github.com/dfmpd/dfmpd/internal/playback/plugin"
)

func main() {
	executable := flag.String("mpv", "mpv", "mpv executable")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: dfmpd-playback-mpv [-mpv executable]")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err := sdk.Serve(ctx, os.Stdin, os.Stdout, "dfmpd-playback-mpv", func(ctx context.Context) (sdk.Backend, error) {
		adapter, err := mpv.Open(ctx, *executable)
		if err != nil {
			return nil, err
		}
		return plugin.NewBackend(adapter), nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
