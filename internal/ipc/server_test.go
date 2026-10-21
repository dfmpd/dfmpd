package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/playback"
	"github.com/dfmpd/dfmpd/internal/source"
)

func startServer(t *testing.T, f playback.Adapter, sources ...map[string]source.Resolver) string {
	t.Helper()
	s, err := Listen(runtimeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var resolvers map[string]source.Resolver
	if len(sources) > 0 {
		resolvers = sources[0]
	}
	c := core.NewWithSources(ctx, f, resolvers)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, s.UnixListener, c) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server shutdown timed out")
		}
		if err := c.Close(); err != nil {
			t.Error(err)
		}
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s.Path()
}
func dial(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(6 * time.Second))
	return conn
}
func readResponse(t *testing.T, reader *bufio.Reader) map[string]json.RawMessage {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(line, &out); err != nil {
		t.Fatalf("response %q: %v", line, err)
	}
	return out
}
func TestMultipleClientsAndFraming(t *testing.T) {
	f := &fakePlayer{}
	path := startServer(t, f)
	a, b := dial(t, path), dial(t, path)
	ra, rb := bufio.NewReader(a), bufio.NewReader(b)
	fmt.Fprint(a, `{"jsonrpc":"2.0","id":1,"method":"playback.`)
	fmt.Fprintln(a, `pause"}`)
	if out := readResponse(t, ra); out["error"] != nil {
		t.Fatal(string(out["error"]))
	}
	fmt.Fprintln(b, `{"jsonrpc":"2.0","id":2,"method":"playback.status"}`)
	out := readResponse(t, rb)
	var state statusResult
	json.Unmarshal(out["result"], &state)
	if !state.Paused {
		t.Fatal("clients do not share playback state")
	}
	// Coalesced notification and request: only the request gets a response.
	fmt.Fprint(b, "{\"jsonrpc\":\"2.0\",\"method\":\"playback.resume\"}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"playback.status\"}\n")
	out = readResponse(t, rb)
	json.Unmarshal(out["result"], &state)
	if string(out["id"]) != "3" || state.Paused {
		t.Fatalf("response=%v", out)
	}
	// Invalid JSON does not destroy framing for the next complete line.
	fmt.Fprint(a, "{\n{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"daemon.info\"}\n")
	if readResponse(t, ra)["error"] == nil {
		t.Fatal("expected parse error")
	}
	if string(readResponse(t, ra)["id"]) != "4" {
		t.Fatal("framing not recovered")
	}
}
func TestDisconnectKeepsAcceptedOperation(t *testing.T) {
	f := &fakePlayer{entered: make(chan struct{}, 1), release: make(chan struct{})}
	path := startServer(t, f)
	a := dial(t, path)
	fmt.Fprintln(a, `{"jsonrpc":"2.0","id":1,"method":"playback.pause"}`)
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("operation not accepted")
	}
	a.Close()
	close(f.release)
	b := dial(t, path)
	fmt.Fprintln(b, `{"jsonrpc":"2.0","id":2,"method":"playback.status"}`)
	out := readResponse(t, bufio.NewReader(b))
	var state statusResult
	json.Unmarshal(out["result"], &state)
	if !state.Paused {
		t.Fatal("disconnect rolled back playback")
	}
}
func TestLimits(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		f := &fakePlayer{}
		path := startServer(t, f)
		conn := dial(t, path)
		fmt.Fprint(conn, strings.Repeat(" ", MaxMessageBytes)+"\n")
		out := readResponse(t, bufio.NewReader(conn))
		if out["error"] == nil {
			t.Fatal("oversize message accepted")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.calls != 0 {
			t.Fatal("oversize frame executed")
		}
	})
	t.Run("connections", func(t *testing.T) {
		path := startServer(t, &fakePlayer{})
		for i := 0; i < MaxConnections; i++ {
			conn := dial(t, path)
			fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"method":"daemon.info"}`)
			if readResponse(t, bufio.NewReader(conn))["error"] != nil {
				t.Fatal("connection rejected below limit")
			}
		}
		excess := dial(t, path)
		var b [1]byte
		if _, err := excess.Read(b[:]); err == nil {
			t.Fatal("connection beyond limit accepted")
		}
	})
	t.Run("request deadline", func(t *testing.T) {
		path := startServer(t, &fakePlayer{release: make(chan struct{})})
		conn := dial(t, path)
		start := time.Now()
		fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"method":"playback.pause"}`)
		out := readResponse(t, bufio.NewReader(conn))
		var err rpcError
		json.Unmarshal(out["error"], &err)
		if err.Code != -32001 || time.Since(start) > 5*time.Second {
			t.Fatalf("deadline: %+v", out)
		}
	})
	t.Run("partial frame", func(t *testing.T) {
		f := &fakePlayer{}
		path := startServer(t, f)
		conn := dial(t, path)
		fmt.Fprint(conn, `{"jsonrpc":"2.0","id":1,"method":"playback.pause"}`)
		conn.(*net.UnixConn).CloseWrite()
		var b [1]byte
		if _, err := conn.Read(b[:]); err == nil {
			t.Fatal("incomplete frame accepted")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.calls != 0 {
			t.Fatal("incomplete frame executed")
		}
	})
}

func TestConcurrentClients(t *testing.T) {
	path := startServer(t, &fakePlayer{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		conn := dial(t, path)
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := bufio.NewReader(conn)
			for _, method := range []string{"playback.pause", "playback.resume", "playback.status"} {
				if _, err := fmt.Fprintf(conn, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":%q}\n", method); err != nil {
					t.Error(err)
					return
				}
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Error(err)
					return
				}
				var out response
				if err := json.Unmarshal(line, &out); err != nil || out.Error != nil {
					t.Errorf("response %q: %v", line, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
func TestResponseSizeLimit(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	// JSON escaping can expand a request ID well beyond its incoming size.
	id, _ := json.Marshal(strings.Repeat("<", MaxMessageBytes/2))
	if err := writeResponse(a, failure(id, -32603, "error")); err == nil {
		t.Fatal("oversized response written")
	}
}
