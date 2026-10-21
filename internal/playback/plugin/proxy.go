package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/dfmpd/dfmpd-sdk/playback"
	"github.com/dfmpd/dfmpd/internal/playback"
)

// Proxy owns a single explicitly launched child. It never retries a request or
// falls back to a built-in adapter. Only one call writes to the stream at a time.
type Proxy struct {
	cmd         *exec.Cmd
	in, out     *os.File
	gate        chan struct{}
	replies     chan response
	broken      chan struct{}
	breakOnce   sync.Once
	breakErr    error
	wait        chan struct{}
	waitErr     error
	readerDone  chan struct{}
	cleanupDone chan struct{}
	cleanupErr  error
	next        atomic.Uint64
	closed      atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

var _ playback.Adapter = (*Proxy)(nil)

// Open accepts a path, not a PATH lookup name. Arguments are passed verbatim.
// ctx bounds startup only; Core/Close own the resulting adapter's lifetime.
func Open(ctx context.Context, executable string, args ...string) (*Proxy, error) {
	if !strings.ContainsRune(executable, os.PathSeparator) {
		return nil, unavailable("plugin executable must be an explicit path")
	}
	path, err := filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	childIn, in, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer childIn.Close()
	out, childOut, err := os.Pipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	defer childOut.Close()
	p := &Proxy{in: in, out: out, gate: make(chan struct{}, 1), replies: make(chan response, 1), broken: make(chan struct{}), wait: make(chan struct{}), readerDone: make(chan struct{}), cleanupDone: make(chan struct{})}
	p.cmd = exec.Command(path, args...)
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = childIn, childOut, os.Stderr
	if err := isolateProcess(p.cmd); err != nil {
		in.Close()
		out.Close()
		return nil, err
	}
	if err := p.cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return nil, fmt.Errorf("start playback plugin: %w", err)
	}
	// Close the parent's copies so child exit is observable as EOF.
	childIn.Close()
	childOut.Close()
	go p.read()
	go p.supervise()
	var info capabilities
	err = p.call(ctx, "plugin.initialize", initialization{Version: Version}, &info, false)
	if err == nil && info.Validate() != nil {
		err = unavailable("incompatible playback plugin capabilities")
	}
	if err != nil {
		p.fail(err)
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Proxy) fail(err error) {
	p.breakOnce.Do(func() {
		p.breakErr = err
		close(p.broken)
		// EOF requests cooperative cleanup from a still-live plugin.
		p.in.Close()
		p.out.Close()
	})
}
func (p *Proxy) read() {
	defer close(p.readerDone)
	r := bufio.NewReaderSize(p.out, MaxFrame)
	var last uint64
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			if err == io.EOF && p.closed.Load() {
				return
			}
			p.fail(unavailable("plugin response stream closed: " + err.Error()))
			return
		}
		var reply response
		if err := sdk.Decode(line, &reply); err != nil {
			p.fail(unavailable("invalid plugin RPC response"))
			return
		}
		id, err := strconv.ParseUint(reply.ID, 10, 64)
		if err != nil || id == 0 || id <= last || id > p.next.Load() || strconv.FormatUint(id, 10) != reply.ID {
			p.fail(unavailable("invalid plugin response ID"))
			return
		}

		last = id
		select {
		case p.replies <- reply:
		case <-p.broken:
			return
		}
	}
}

func (p *Proxy) call(ctx context.Context, method string, params, result any, closing bool) error {
	if p.closed.Load() && !closing {
		return &playback.Error{Code: playback.Closed, Message: "playback plugin is closed"}
	}
	select {
	case p.gate <- struct{}{}:
	case <-p.broken:
		return p.breakErr
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.gate }()
	if p.closed.Load() && !closing {
		return &playback.Error{Code: playback.Closed, Message: "playback plugin is closed"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-p.broken:
		return p.breakErr
	default:
	}
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := strconv.FormatUint(p.next.Add(1), 10)
	deadline, _ := ctx.Deadline()
	line, err := sdk.Frame(request{JSONRPC: "2.0", ID: id, Method: method, Params: data, Deadline: deadline.Add(time.Millisecond - 1).UnixMilli()})
	if err != nil {
		return err
	}
	// A blocked/partial pipe write cannot safely be resumed for another frame.
	// Closing the session on cancellation bounds the writer; no replay occurs.
	written := make(chan error, 1)
	go func() { _, e := p.in.Write(line); written <- e }()
	select {
	case err := <-written:
		if err != nil {
			p.fail(unavailable("write plugin request: " + err.Error()))
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return p.breakErr
		}
	case <-ctx.Done():
		p.fail(unavailable("plugin request write interrupted"))
		<-written
		return ctx.Err()
	}
	for {
		var reply response
		select {
		case reply = <-p.replies:
		case <-ctx.Done():
			p.fail(unavailable("plugin RPC interrupted; outcome unknown"))
			return ctx.Err()
		case <-p.readerDone:
			select {
			case reply = <-p.replies:
			default:
				p.fail(unavailable("plugin response stream closed"))
				return p.breakErr
			}
		case <-p.broken:
			// The final shutdown response may precede EOF in the buffered stream.
			select {
			case reply = <-p.replies:
			default:
				return p.breakErr
			}
		}
		if err := ctx.Err(); err != nil {
			p.fail(unavailable("plugin RPC interrupted; outcome unknown"))
			return err
		}
		if reply.ID != id {
			continue
		} // late response to an earlier timed-out call
		if reply.Error != nil {
			if reply.Error.Data != nil {
				converted := fromSDKError(reply.Error.Data)
				switch converted.Code {
				case playback.TimedOut, playback.Canceled, playback.Unavailable, playback.Closed:
					p.fail(unavailable("playback backend unavailable"))
				}
				return converted
			}
			return &playback.Error{Code: playback.Rejected, Message: reply.Error.Message}
		}
		if err := sdk.Decode(reply.Result, result); err != nil {
			p.fail(unavailable("invalid plugin result"))
			return p.breakErr
		}
		return nil
	}
}
func (p *Proxy) operation(ctx context.Context, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, RPCTimeout)
	defer cancel()
	return p.call(ctx, method, params, result, false)
}
func (p *Proxy) command(ctx context.Context, method string, params any) error {
	var ack accepted
	if err := p.operation(ctx, method, params, &ack); err != nil {
		return err
	}
	if !ack.Accepted {
		p.fail(unavailable("plugin did not acknowledge command"))
		return p.breakErr
	}
	return nil
}
func (p *Proxy) Play(ctx context.Context, in playback.Input) error {
	return p.command(ctx, "playback.play", sdk.Input{Location: in.Location})
}
func (p *Proxy) Pause(ctx context.Context) error { return p.command(ctx, "playback.pause", struct{}{}) }
func (p *Proxy) Resume(ctx context.Context) error {
	return p.command(ctx, "playback.resume", struct{}{})
}
func (p *Proxy) Stop(ctx context.Context) error { return p.command(ctx, "playback.stop", struct{}{}) }
func (p *Proxy) Seek(ctx context.Context, seconds float64) error {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return &playback.Error{Code: playback.InvalidInput, Message: "invalid seek seconds"}
	}
	return p.command(ctx, "playback.seek", sdk.SeekParams{Seconds: seconds})
}
func (p *Proxy) Status(ctx context.Context) (playback.Status, error) {
	var wire sdk.Status
	err := p.operation(ctx, "playback.status", struct{}{}, &wire)
	return fromSDKStatus(wire), err
}

// Close is idempotent. Broken sessions are already being collected by supervise;
// healthy sessions first get a bounded opportunity to shut down cooperatively.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
		defer cancel()
		var ack accepted
		err := p.call(ctx, "plugin.shutdown", struct{}{}, &ack, true)
		if err == nil && !ack.Accepted {
			err = unavailable("plugin shutdown not acknowledged")
		}
		p.in.Close()
		if err != nil {
			p.fail(unavailable("plugin shutdown failed"))
		}
		select {
		case <-p.cleanupDone:
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
			p.fail(unavailable("plugin shutdown deadline exceeded"))
			<-p.cleanupDone // supervisor has its own finite TERM/KILL/reap budget
		}
		p.closeErr = errors.Join(err, p.cleanupErr)
		select {
		case <-p.wait:
			p.closeErr = errors.Join(p.closeErr, p.waitErr)
		default:
		}
	})
	return p.closeErr
}
