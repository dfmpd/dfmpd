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
		io.Copy(io.Discard, conn)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := Call(ctx, path, "test", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation=%v", err)
	}
	<-done
}
