//go:build linux

package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	sdk "github.com/dfmpd/dfmpd-sdk/playback"
	"github.com/dfmpd/dfmpd/internal/playback"
)

type treeEvent struct {
	Event string
	PID   int
}

// The separate control socket establishes readiness/request receipt without
// sleeps. The grandchild deliberately retains stdout and ignores SIGTERM.
func TestTreeProcess(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-3] != "--tree" {
		return
	}
	mode, path := args[len(args)-2], args[len(args)-1]
	conn, err := net.Dial("unix", path)
	if err != nil {
		os.Exit(10)
	}
	defer conn.Close()
	if mode == "descendant" {
		signal.Ignore(syscall.SIGTERM)
		json.NewEncoder(conn).Encode(treeEvent{"descendant", os.Getpid()})
		io.Copy(io.Discard, conn)
		os.Exit(0)
	}
	signal.Ignore(syscall.SIGPIPE)
	if mode == "stall" || mode == "late" || mode == "shutdown-stall" {
		signal.Ignore(syscall.SIGTERM)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestTreeProcess$", "--", "--tree", "descendant", path)
	child.Stderr = os.Stderr
	if mode != "eof" {
		child.Stdout = os.Stdout
	}
	if child.Start() != nil {
		os.Exit(11)
	}
	json.NewEncoder(conn).Encode(treeEvent{"root", os.Getpid()})
	reader := bufio.NewReader(os.Stdin)
	control := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var req request
		if json.Unmarshal(line, &req) != nil {
			os.Exit(12)
		}
		f, _ := os.OpenFile(path+".log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintln(f, req.Method)
		f.Close()
		var result any = accepted{Accepted: true}
		if req.Method == "plugin.initialize" {
			result = sdk.DefaultCapabilities("tree")
		} else if req.Method != "plugin.shutdown" || mode == "shutdown-stall" {
			json.NewEncoder(conn).Encode(treeEvent{"request", os.Getpid()})
			if _, err := control.ReadBytes('\n'); err != nil {
				os.Exit(0)
			}
			switch mode {
			case "crash":
				os.Exit(7)
			case "malformed":
				fmt.Fprintln(os.Stdout, "bad response")
				io.Copy(io.Discard, conn)
				os.Exit(0)
			case "eof":
				os.Stdout.Close()
				io.Copy(io.Discard, conn)
				os.Exit(0)
			case "stall", "shutdown-stall":
				io.Copy(io.Discard, conn)
				os.Exit(0)
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

// Only the isolated test executable becomes a subreaper, so it can assert and
// reap orphaned grandchildren even under a container PID 1 that doesn't reap.
// Production leaves orphan reaping to init/systemd, without global waitpid(-1).
func testSubreaper(t *testing.T) {
	t.Helper()
	var old int32
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, 37 /* GET_CHILD_SUBREAPER */, uintptr(unsafe.Pointer(&old)), 0, 0, 0, 0); e != 0 {
		t.Fatal(e)
	}
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, 36 /* SET_CHILD_SUBREAPER */, 1, 0, 0, 0, 0); e != 0 {
		t.Fatal(e)
	}
	t.Cleanup(func() { syscall.Syscall6(syscall.SYS_PRCTL, 36, uintptr(old), 0, 0, 0, 0) })
}

type treeFixture struct {
	p          *Proxy
	conn       net.Conn
	decoder    *json.Decoder
	descendant int
	path       string
	finishOnce sync.Once
}

func startTree(t *testing.T, mode string) *treeFixture {
	t.Helper()
	testSubreaper(t)
	dir, err := os.MkdirTemp("", "dfmpd-tree-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	type peer struct {
		conn    net.Conn
		event   treeEvent
		decoder *json.Decoder
		err     error
	}
	peers := make(chan peer, 2)
	go func() {
		for range 2 {
			conn, err := listener.Accept()
			if err != nil {
				peers <- peer{err: err}
				return
			}
			conn.SetDeadline(time.Now().Add(15 * time.Second))
			d := json.NewDecoder(conn)
			var e treeEvent
			err = d.Decode(&e)
			peers <- peer{conn, e, d, err}
		}
	}()
	executable, _ := os.Executable()
	p, err := Open(context.Background(), executable, "-test.run=^TestTreeProcess$", "--", "--tree", mode, path)
	if err != nil {
		t.Fatal(err)
	}
	f := &treeFixture{p: p, path: path}
	t.Cleanup(func() { f.finish(t) })
	for range 2 {
		select {
		case peer := <-peers:
			if peer.err != nil {
				t.Fatal(peer.err)
			}
			t.Cleanup(func() { peer.conn.Close() })
			if peer.event.Event == "root" {
				f.conn, f.decoder = peer.conn, peer.decoder
			} else {
				f.descendant = peer.event.PID
			}
		case <-time.After(10 * time.Second):
			t.Fatal("tree readiness timeout")
		}
	}
	return f
}
func (f *treeFixture) request(t *testing.T) {
	t.Helper()
	var e treeEvent
	if err := f.decoder.Decode(&e); err != nil || e.Event != "request" {
		t.Fatalf("request readiness: %+v %v", e, err)
	}
}
func (f *treeFixture) release(t *testing.T) {
	t.Helper()
	if _, err := fmt.Fprintln(f.conn, "go"); err != nil {
		t.Fatal(err)
	}
}
func (f *treeFixture) finish(t *testing.T) {
	t.Helper()
	f.finishOnce.Do(func() {
		_ = f.p.Close()
		select {
		case <-f.p.cleanupDone:
		case <-time.After(8 * time.Second):
			t.Fatal("cleanup stuck")
		}
		select {
		case <-f.p.wait:
		default:
			t.Error("plugin child not reaped")
		}
		if f.descendant == 0 {
			return
		}
		done := make(chan error, 1)
		go func() {
			var status syscall.WaitStatus
			_, err := syscall.Wait4(f.descendant, &status, 0, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("grandchild survived group cleanup")
		}
	})
}
func resultWithin(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(8 * time.Second):
		t.Fatal("RPC/Close blocked")
		return nil
	}
}

func TestProcessGroupFailures(t *testing.T) {
	for _, mode := range []string{"crash", "kill", "malformed", "eof"} {
		t.Run(mode, func(t *testing.T) {
			f := startTree(t, mode)
			done := make(chan error, 1)
			go func() { done <- f.p.Stop(context.Background()) }()
			f.request(t)
			waiting := make(chan error, 1)
			go func() { _, err := f.p.Status(context.Background()); waiting <- err }()
			if mode == "kill" {
				if err := f.p.cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				f.release(t)
			}
			if err := resultWithin(t, done); !errors.Is(err, &playback.Error{Code: playback.Unavailable}) {
				t.Fatalf("failure=%v", err)
			}
			if err := resultWithin(t, waiting); !errors.Is(err, &playback.Error{Code: playback.Unavailable}) {
				t.Fatalf("waiting RPC=%v", err)
			}
			// Reaping must start without requiring daemon/Core.Close.
			select {
			case <-f.p.cleanupDone:
			case <-time.After(5 * time.Second):
				t.Fatal("no automatic cleanup")
			}
			if err := f.p.Stop(context.Background()); err == nil {
				t.Fatal("failed session reported success")
			}
			f.finish(t)
		})
	}
}

func TestProxyLateResponseNoRetry(t *testing.T) {
	f := startTree(t, "late")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.p.Play(ctx, playback.Input{Location: "/test.flac"}) }()
	f.request(t) // the mutation has reached the plugin; it may already execute
	cancel()
	if err := resultWithin(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Deterministically model a response already buffered at the cancellation
	// boundary. Even a valid accepted reply cannot revive the retired session.
	f.p.replies <- response{JSONRPC: "2.0", ID: "2", Result: json.RawMessage(`{"accepted":true}`)}
	fmt.Fprintln(f.conn, "go") // allow the wire reply if cleanup has not already killed the peer
	if _, err := f.p.Status(context.Background()); !errors.Is(err, &playback.Error{Code: playback.Unavailable}) {
		t.Fatalf("late response reused: %v", err)
	}
	f.finish(t)
	data, _ := os.ReadFile(f.path + ".log")
	if strings.Count(string(data), "playback.play\n") != 1 || strings.Contains(string(data), "playback.status") {
		t.Fatalf("replayed request: %s", data)
	}
}

func TestProcessGroupResponseDeadline(t *testing.T) {
	f := startTree(t, "stall")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.p.Stop(ctx) }()
	f.request(t)
	f.release(t)
	if err := resultWithin(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	f.finish(t)
}

func TestProcessGroupShutdown(t *testing.T) {
	for _, mode := range []string{"normal", "shutdown-stall"} {
		t.Run(mode, func(t *testing.T) {
			f := startTree(t, mode)
			done := make(chan error, 4)
			for range 4 {
				go func() { done <- f.p.Close() }()
			}
			if mode == "shutdown-stall" {
				f.request(t)
				f.release(t)
			}
			err := resultWithin(t, done)
			if (err == nil) != (mode == "normal") {
				t.Fatalf("close=%v", err)
			}
			for range 3 {
				if other := resultWithin(t, done); (other == nil) != (err == nil) {
					t.Fatalf("concurrent Close=%v", other)
				}
			}
			if again := f.p.Close(); (again == nil) != (err == nil) {
				t.Fatalf("non-idempotent close: %v", again)
			}
			f.finish(t)
		})
	}
}

func TestProcessGroupIdleCrash(t *testing.T) {
	f := startTree(t, "normal")
	if err := f.p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// No RPC and no Close: the grandchild keeps stdout open, so EOF alone
	// cannot detect this failure until independent process supervision runs.
	select {
	case <-f.p.cleanupDone:
	case <-time.After(5 * time.Second):
		t.Fatal("idle crash not collected")
	}
	f.finish(t)
}
