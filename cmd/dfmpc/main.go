// dfmpc is an independent client; it never starts or manages the daemon.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/ipc"
	"github.com/dfmpd/dfmpd/internal/media"
	"github.com/dfmpd/dfmpd/internal/source/local"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("dfmpc", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", "", "daemon Unix socket (default: XDG_RUNTIME_DIR/dfmpd/control.sock)")
	compact := flags.Bool("json", false, "compact JSON output")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: dfmpc [--socket PATH] [--json] status|play FILE|pause|resume|stop|seek SECONDS|next|previous|info\n       dfmpc [options] queue add FILE|list [--offset N --limit N]|remove ID|play ID")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	method, params, err := command(flags.Args())
	if err != nil {
		return err
	}
	if *socket == "" {
		runtime := os.Getenv("XDG_RUNTIME_DIR")
		if !filepath.IsAbs(runtime) {
			return errors.New("XDG_RUNTIME_DIR must be absolute, or supply --socket")
		}
		*socket = filepath.Join(runtime, "dfmpd", "control.sock")
	}
	var result json.RawMessage
	if err := ipc.Call(ctx, *socket, method, params, &result); err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	if !*compact {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(result)
}
func command(args []string) (string, any, error) {
	invalid := func() (string, any, error) { return "", nil, errors.New("invalid command; use dfmpc --help") }
	if len(args) == 0 {
		return invalid()
	}
	switch args[0] {
	case "info", "status", "pause", "resume", "stop", "next", "previous":
		if len(args) != 1 {
			return invalid()
		}
		switch args[0] {
		case "info":
			return "daemon.info", nil, nil
		case "next", "previous":
			return "queue." + args[0], nil, nil
		default:
			return "playback." + args[0], nil, nil
		}
	case "play":
		if len(args) != 2 {
			return invalid()
		}
		identity, err := local.Identity(args[1])
		if err != nil {
			return "", nil, err
		}
		return "playback.play", struct {
			Location string `json:"location"`
		}{identity.ItemID}, nil
	case "seek":
		if len(args) != 2 {
			return invalid()
		}
		seconds, err := strconv.ParseFloat(args[1], 64)
		if err != nil || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return "", nil, errors.New("seek requires finite, nonnegative seconds")
		}
		return "playback.seek", struct {
			Seconds float64 `json:"seconds"`
		}{seconds}, nil
	case "queue":
		if len(args) < 2 {
			return invalid()
		}
		switch args[1] {
		case "add":
			if len(args) != 3 {
				return invalid()
			}
			identity, err := local.Identity(args[2])
			if err != nil {
				return "", nil, err
			}
			return "queue.add", struct {
				Identity media.Identity `json:"identity"`
			}{identity}, nil
		case "remove", "play":
			if len(args) != 3 || args[2] == "" {
				return invalid()
			}
			return "queue." + args[1], struct {
				EntryID string `json:"entry_id"`
			}{args[2]}, nil
		case "list":
			f := flag.NewFlagSet("queue list", flag.ContinueOnError)
			f.SetOutput(io.Discard)
			offset := f.Int("offset", 0, "offset")
			limit := f.Int("limit", core.DefaultPageLimit, "limit")
			if err := f.Parse(args[2:]); err != nil {
				return "", nil, err
			}
			if f.NArg() != 0 || *offset < 0 || *limit < 1 || *limit > core.MaxPageLimit {
				return invalid()
			}
			return "queue.list", struct {
				Offset int `json:"offset"`
				Limit  int `json:"limit"`
			}{*offset, *limit}, nil
		}
	}
	return invalid()
}
