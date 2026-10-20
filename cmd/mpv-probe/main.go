// mpv-probe is an interactive M0 experiment, not the DFMPD daemon.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dot-1245/dfmpd/internal/playback/mpv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() (err error) {
	executable := flag.String("mpv", "mpv", "mpv executable")
	flag.Parse()
	if flag.NArg() != 1 {
		return fmt.Errorf("usage: mpv-probe [-mpv executable] AUDIO_FILE")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	p, err := mpv.Start(ctx, *executable)
	if err != nil {
		return err
	}
	defer func() {
		if e := p.Close(); err == nil {
			err = e
		}
	}()
	if err := p.Load(ctx, flag.Arg(0)); err != nil {
		return err
	}
	fmt.Println("Load accepted. Commands: play, pause, resume, stop, status, quit")
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.Done():
			return fmt.Errorf("mpv exited unexpectedly")
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			var e error
			switch strings.TrimSpace(line) {
			case "play":
				e = p.Load(ctx, flag.Arg(0))
				if e == nil {
					e = p.SetPaused(ctx, false)
				}
			case "pause":
				e = p.SetPaused(ctx, true)
			case "resume":
				e = p.SetPaused(ctx, false)
			case "stop":
				e = p.Stop(ctx)
			case "status":
				var s mpv.Status
				s, e = p.Status(ctx)
				if e != nil {
					fmt.Fprintln(os.Stderr, e)
					continue
				}
				if e == nil {
					e = json.NewEncoder(os.Stdout).Encode(s)
				}
			case "quit":
				return nil
			default:
				fmt.Println("Commands: play, pause, resume, stop, status, quit")
			}
			if e != nil {
				return e
			}
		}
	}
}
