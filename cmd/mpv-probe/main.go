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
	"strconv"
	"strings"
	"syscall"

	"github.com/dot-1245/dfmpd/internal/playback"
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
	p, err := mpv.Open(ctx, *executable)
	if err != nil {
		return err
	}
	defer func() {
		if e := p.Close(); err == nil {
			err = e
		}
	}()
	var player playback.Adapter = p
	if err := player.Play(ctx, playback.Input{Location: flag.Arg(0)}); err != nil {
		return err
	}
	fmt.Println("Load accepted. Commands: play, pause, resume, stop, status, seek SECONDS, quit")
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
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			switch fields[0] {
			case "play":
				e = player.Play(ctx, playback.Input{Location: flag.Arg(0)})
			case "pause":
				e = player.Pause(ctx)
			case "resume":
				e = player.Resume(ctx)
			case "stop":
				e = player.Stop(ctx)
			case "status":
				var s playback.Status
				s, e = player.Status(ctx)
				if e != nil {
					fmt.Fprintln(os.Stderr, e)
					continue
				}
				if e == nil {
					// Keep the M0 diagnostic output shape for existing probe users.
					output := struct {
						Idle          bool     `json:"idle"`
						Paused        bool     `json:"paused"`
						Position      *float64 `json:"position_seconds,omitempty"`
						PlaybackError string   `json:"playback_error,omitempty"`
					}{Idle: s.Idle, Paused: s.Paused, Position: s.Position}
					if s.Failure != nil {
						output.PlaybackError = s.Failure.Message
					}
					e = json.NewEncoder(os.Stdout).Encode(output)
				}
			case "seek":
				if len(fields) != 2 {
					fmt.Fprintln(os.Stderr, "usage: seek SECONDS")
					continue
				}
				var seconds float64
				seconds, e = strconv.ParseFloat(fields[1], 64)
				if e == nil {
					e = player.Seek(ctx, seconds)
				}
				if e != nil {
					fmt.Fprintln(os.Stderr, e)
					continue
				}
			case "quit":
				return nil
			default:
				fmt.Println("Commands: play, pause, resume, stop, status, seek SECONDS, quit")
			}
			if e != nil {
				return e
			}
		}
	}
}
