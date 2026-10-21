package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/dot-1245/dfmpd/internal/core"
)

const (
	MaxMessageBytes = 64 * 1024 // including the terminating newline
	MaxConnections  = 16
	ReadTimeout     = 30 * time.Second
	WriteTimeout    = 3 * time.Second
)

// Serve handles at most one request per connection at a time. Each admitted
// request uses the daemon context, not connection lifetime: disconnecting never
// stops playback or rolls back an accepted operation. The caller owns Core.
func Serve(ctx context.Context, listener net.Listener, c *core.Core) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var workers sync.WaitGroup
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		_ = listener.Close()
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
	}()
	defer func() { cancel(); <-shutdownDone; workers.Wait() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		if ctx.Err() != nil || len(connections) >= MaxConnections {
			mu.Unlock()
			conn.Close()
			continue
		}
		connections[conn] = struct{}{}
		workers.Add(1)
		mu.Unlock()
		go func() {
			defer workers.Done()
			defer func() { conn.Close(); mu.Lock(); delete(connections, conn); mu.Unlock() }()
			serveConnection(ctx, conn, c)
		}()
	}
}
func serveConnection(ctx context.Context, conn net.Conn, c *core.Core) {
	reader := bufio.NewReaderSize(conn, MaxMessageBytes)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(ReadTimeout)); err != nil {
			return
		}
		frame, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				_ = writeResponse(conn, failure(nil, -32600, "Message exceeds size limit"))
			}
			// EOF without a newline is an incomplete frame and is never executed.
			return
		}
		if ctx.Err() != nil {
			return
		}
		out := handle(ctx, c, frame)
		if out != nil && writeResponse(conn, out) != nil {
			return
		}
	}
}
func writeResponse(conn net.Conn, out *response) error {
	if err := conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if len(data)+1 > MaxMessageBytes {
		data, _ = json.Marshal(failure(out.ID, -32603, "Response exceeds size limit"))
	}
	if len(data)+1 > MaxMessageBytes {
		return errors.New("response ID exceeds size limit")
	}
	data = append(data, '\n')
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
