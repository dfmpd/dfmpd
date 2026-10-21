package ipc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientValidation(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		success     bool
	}{
		{"valid", `{"jsonrpc":"2.0","id":"dfmpc-1","result":{"ok":true}}`, true},
		{"rpc error", `{"jsonrpc":"2.0","id":"dfmpc-1","error":{"code":-32601,"message":"unknown"}}`, false},
		{"wrong id", `{"jsonrpc":"2.0","id":"other","result":{}}`, false},
		{"wrong version", `{"jsonrpc":"1.0","id":"dfmpc-1","result":{}}`, false},
		{"both", `{"jsonrpc":"2.0","id":"dfmpc-1","result":{},"error":null}`, false},
		{"null error", `{"jsonrpc":"2.0","id":"dfmpc-1","error":null}`, false},
		{"malformed", "{", false},
		{"oversized", strings.Repeat(" ", MaxMessageBytes), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(runtimeDir(t), "peer")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				bufio.NewReader(conn).ReadBytes('\n')
				fmt.Fprintln(conn, tc.reply)
			}()
			var result any
			err = Call(context.Background(), path, "test", nil, &result)
			if (err == nil) != tc.success {
				t.Fatalf("err=%v", err)
			}
			if tc.name == "rpc error" {
				var rpc *RPCError
				if !errors.As(err, &rpc) || rpc.Code != -32601 {
					t.Fatalf("not a structured error: %v", err)
				}
			}
			<-done
		})
	}
}
func TestClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path, done := silentClientPeer(t, cancel, false)
	if err := Call(ctx, path, "playback.stop", nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation=%v", err)
	}
	<-done
}

func TestClientDeadline(t *testing.T) {
	path, done := silentClientPeer(t, func() {}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// The peer consumes the mutation but never acknowledges it. A timeout does
	// not establish whether it executed; Call must not retry the mutation.
	if err := Call(ctx, path, "playback.stop", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline=%v", err)
	}
	<-done
}

func TestClientTransportFailure(t *testing.T) {
	path, done := silentClientPeer(t, func() {}, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Call(ctx, path, "playback.stop", nil, nil)
	if !errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Errorf("transport failure=%v, context=%v", err, ctx.Err())
	}
	<-done
}

// silentClientPeer consumes exactly one request and never sends a response.
// afterRequest synchronizes explicit cancellation with receipt of the request.
func silentClientPeer(t *testing.T, afterRequest func(), disconnect bool) (string, <-chan struct{}) {
	t.Helper()
	path := filepath.Join(runtimeDir(t), "peer")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	// Bound test failures independently of the client cancellation mechanism.
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		// Reject any second connection: retrying must not silently succeed.
		listener.Close()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Errorf("peer deadline: %v", err)
			return
		}
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes('\n'); err != nil {
			t.Errorf("request: %v", err)
			return
		}
		afterRequest()
		if disconnect {
			return
		}
		n, err := io.Copy(io.Discard, reader)
		if err != nil || n != 0 {
			t.Errorf("expected client close without another request: bytes=%d, err=%v", n, err)
		}
	}()
	return path, done
}
