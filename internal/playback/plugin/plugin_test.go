package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/media"
	"github.com/dfmpd/dfmpd/internal/playback"
	"github.com/dfmpd/dfmpd/internal/source"
	"github.com/dfmpd/dfmpd/internal/source/local"
)

type testAdapter struct {
	paused   bool
	idle     bool
	position float64
	log      string
}

func (a *testAdapter) record(op string) {
	f, err := os.OpenFile(a.log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, op)
}
func (a *testAdapter) Play(_ context.Context, in playback.Input) error {
	if in.Location == "" {
		return invalidParams()
	}
	a.record("play")
	a.idle = false
	a.paused = false
	return nil
}
func (a *testAdapter) Pause(context.Context) error  { a.record("pause"); a.paused = true; return nil }
func (a *testAdapter) Resume(context.Context) error { a.record("resume"); a.paused = false; return nil }
func (a *testAdapter) Stop(context.Context) error   { a.record("stop"); a.idle = true; return nil }
func (a *testAdapter) Seek(_ context.Context, seconds float64) error {
	a.record("seek")
	a.position = seconds
	return nil
}
func (a *testAdapter) Status(context.Context) (playback.Status, error) {
	a.record("status")
	state := playback.Playing
	if a.idle {
		state = playback.Idle
	} else if a.paused {
		state = playback.Paused
	}
	p := a.position
	return playback.Status{State: state, Idle: a.idle, Paused: a.paused, Position: &p}, nil
}
func (a *testAdapter) Close() error { a.record("close"); return nil }

// A real child process exercises pipes, EOF and Wait, without requiring mpv.
func TestPluginProcess(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-3] != "--plugin-test" {
		return
	}
	mode, log := args[len(args)-2], args[len(args)-1]
	if mode == "serve" || mode == "open-error" {
		err := Serve(context.Background(), os.Stdin, os.Stdout, func(context.Context) (playback.Adapter, error) {
			if mode == "open-error" {
				return nil, errors.New("backend startup failed")
			}
			return &testAdapter{idle: true, log: log}, nil
		})
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if mode == "stall-init" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var req request
		if json.Unmarshal(line, &req) != nil {
			os.Exit(3)
		}
		if mode == "stall-shutdown" && req.Method == "plugin.shutdown" {
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		f, _ := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintln(f, req.Method)
		f.Close()
		var result any = accepted{true}
		if req.Method == "plugin.initialize" {
			c := capabilities{Version, "playback", "fake", operations, []string{"local-file"}}
			switch mode {
			case "version":
				c.Version++
			case "kind":
				c.Kind = "source"
			case "operations":
				c.Operations = []string{"play"}
			case "inputs":
				c.Inputs = []string{"url"}
			}
			result = c
		} else if req.Method != "plugin.shutdown" {
			switch mode {
			case "crash":
				os.Exit(7)
			case "malformed":
				fmt.Fprintln(os.Stdout, "not JSON")
				continue
			case "wrong-id":
				req.ID = "9999"
			case "both":
				fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%q,\"result\":{},\"error\":null}\n", req.ID)
				continue
			case "oversized":
				fmt.Fprintln(os.Stdout, strings.Repeat("x", MaxFrame))
				continue
			case "delay":
				if req.Method == "playback.play" {
					time.Sleep(100 * time.Millisecond)
				}
			}
			if req.Method == "playback.status" {
				result = playback.Status{State: playback.Idle, Idle: true}
			}
		}
		data, _ := json.Marshal(result)
		json.NewEncoder(os.Stdout).Encode(response{JSONRPC: "2.0", ID: req.ID, Result: data})
		if req.Method == "plugin.shutdown" {
			os.Exit(0)
		}
	}
}

func startTestPlugin(t *testing.T, mode string) (*Proxy, string) {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "operations")
	p, err := Open(context.Background(), path, "-test.run=^TestPluginProcess$", "--", "--plugin-test", mode, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, log
}
func TestProxyOperations(t *testing.T) {
	p, log := startTestPlugin(t, "serve")
	ctx := context.Background()
	for _, op := range []func() error{
		func() error { return p.Play(ctx, playback.Input{Location: "/test.flac"}) },
		func() error { return p.Pause(ctx) },
		func() error {
			s, e := p.Status(ctx)
			if !s.Paused {
				t.Errorf("not paused: %+v", s)
			}
			return e
		},
		func() error { return p.Resume(ctx) },
		func() error { return p.Seek(ctx, 12) },
		func() error {
			s, e := p.Status(ctx)
			if s.State != playback.Playing || s.Position == nil || *s.Position != 12 {
				t.Errorf("status=%+v", s)
			}
			return e
		},
		func() error { return p.Stop(ctx) },
	} {
		if err := op(); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.wait:
	default:
		t.Fatal("child not reaped")
	}
	data, _ := os.ReadFile(log)
	if strings.Count(string(data), "close\n") != 1 {
		t.Fatalf("close count: %s", data)
	}
	if err := p.Stop(ctx); !errors.Is(err, &playback.Error{Code: playback.Closed}) {
		t.Fatalf("closed=%v", err)
	}
}
func TestProxyInitializationFailure(t *testing.T) {
	path, _ := os.Executable()
	for _, mode := range []string{"version", "kind", "operations", "inputs", "open-error", "stall-init"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			p, err := Open(ctx, path, "-test.run=^TestPluginProcess$", "--", "--plugin-test", mode, filepath.Join(t.TempDir(), "log"))
			if err == nil {
				p.Close()
				t.Fatal("invalid initialization succeeded")
			}
			if mode == "stall-init" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		})
	}
	if _, err := Open(context.Background(), "plugin"); err == nil {
		t.Fatal("PATH lookup allowed")
	}
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing executable accepted")
	}
}
func TestProxyBrokenResponse(t *testing.T) {
	for _, mode := range []string{"crash", "malformed", "wrong-id", "both", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := startTestPlugin(t, mode)
			if err := p.Stop(context.Background()); !errors.Is(err, &playback.Error{Code: playback.Unavailable}) {
				t.Fatalf("error=%v", err)
			}
			if _, err := p.Status(context.Background()); err == nil {
				t.Fatal("broken session reused")
			}
		})
	}
}
func TestProxyLateResponseNoRetry(t *testing.T) {
	p, log := startTestPlugin(t, "delay")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := p.Play(ctx, playback.Input{Location: "/test.flac"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("play=%v", err)
	}
	// The late accepted response must not be decoded as the next status result.
	s, err := p.Status(context.Background())
	if err != nil || s.State != playback.Idle {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	if strings.Count(string(data), "playback.play\n") != 1 || strings.Count(string(data), "playback.status\n") != 1 {
		t.Fatalf("requests were replayed: %s", data)
	}
}

func TestPluginCrashPreservesCoreQueue(t *testing.T) {
	p, _ := startTestPlugin(t, "crash")
	c := core.NewWithSources(context.Background(), p, map[string]source.Resolver{local.ID: local.Resolver{}})
	defer c.Close()
	path := filepath.Join(t.TempDir(), "audio.flac")
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := c.Queue(context.Background(), core.Command{Operation: core.QueueAdd, Identity: media.Identity{SourceID: local.ID, ItemID: path}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Queue(context.Background(), core.Command{Operation: core.QueuePlay, EntryID: page.Entries[0].EntryID})
	if err == nil {
		t.Fatal("crash not reported")
	}
	page, err = c.Queue(context.Background(), core.Command{Operation: core.QueueList, Limit: 20})
	if err != nil || len(page.Entries) != 1 || page.Outcome != "unknown" {
		t.Fatalf("queue=%+v err=%v", page, err)
	}
}

func TestProxyShutdownDeadline(t *testing.T) {
	p, _ := startTestPlugin(t, "stall-shutdown")
	start := time.Now()
	if err := p.Close(); err == nil {
		t.Fatal("unacknowledged shutdown succeeded")
	}
	if time.Since(start) > ShutdownTimeout+2*time.Second {
		t.Fatal("shutdown exceeded bounded cleanup time")
	}
	select {
	case <-p.wait:
	default:
		t.Fatal("unresponsive child was not reaped")
	}
}
