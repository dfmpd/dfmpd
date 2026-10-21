// Package mpv implements the mpv playback adapter and its M0 IPC transport.
package mpv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type commandError struct{ message string }

func (e *commandError) Error() string { return "mpv: " + e.message }

type response struct {
	RequestID uint64          `json:"request_id"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
	Event     string          `json:"event"`
	Reason    string          `json:"reason"`
	FileError string          `json:"file_error"`
}

// Probe owns one child and one IPC connection. Operations are serialized.
type Probe struct {
	cmd           *exec.Cmd
	dir           string
	conn          net.Conn
	done          chan struct{}
	exitErr       error // published by closing done
	responses     chan response
	readDone      chan struct{}
	readErr       error // published by closing readDone
	mu            sync.Mutex
	nextID        uint64
	eventMu       sync.Mutex
	playbackError string
	loading       bool
	closeOnce     sync.Once
	closeErr      error
}

// Start uses a private socket and waits for an actual IPC response.
// The context bounds startup; Close owns the child's subsequent lifetime.
func Start(ctx context.Context, executable string) (_ *Probe, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	base := os.Getenv("XDG_RUNTIME_DIR")
	if info, e := os.Stat(base); e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		base = ""
	}
	dir, err := os.MkdirTemp(base, "dfmpd-")
	if err != nil && base != "" {
		dir, err = os.MkdirTemp("", "dfmpd-")
	}
	if err != nil {
		return nil, err
	}
	p := &Probe{dir: dir, done: make(chan struct{}), responses: make(chan response, 1), readDone: make(chan struct{})}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	socket := filepath.Join(dir, "ipc")
	p.cmd = exec.Command(executable, "--no-config", "--idle=yes", "--vid=no", "--input-terminal=no", "--input-ipc-server="+socket)
	p.cmd.Stderr = os.Stderr
	if err = p.cmd.Start(); err != nil {
		p.cmd = nil
		return nil, fmt.Errorf("start mpv: %w", err)
	}
	go func() { p.exitErr = p.cmd.Wait(); close(p.done) }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.conn, err = (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("mpv readiness: %w", ctx.Err())
		case <-p.done:
			return nil, fmt.Errorf("mpv exited during startup: %v", p.exitErr)
		case <-ticker.C:
		}
	}
	go p.read()
	if err = p.call(ctx, []any{"get_property", "mpv-version"}, nil); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Probe) read() {
	defer close(p.readDone)
	scanner := bufio.NewScanner(p.conn)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var r response
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			p.readErr = fmt.Errorf("decode IPC: %w", err)
			return
		}
		if r.Event != "" {
			p.eventMu.Lock()
			switch r.Event {
			case "start-file":
				p.loading = true
				p.playbackError = ""
			case "file-loaded", "end-file":
				p.loading = false
			}
			p.eventMu.Unlock()
			if r.Event == "end-file" && r.Reason == "error" {
				p.eventMu.Lock()
				p.playbackError = r.FileError
				if p.playbackError == "" {
					p.playbackError = "playback failed"
				}
				p.eventMu.Unlock()
			}
			continue
		}
		select {
		case p.responses <- r:
		case <-p.done:
			p.readErr = errors.New("mpv exited")
			return
		}
	}
	p.readErr = scanner.Err()
	if p.readErr == nil {
		p.readErr = errors.New("mpv IPC disconnected")
	}
}

func (p *Probe) call(ctx context.Context, command []any, result any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	p.nextID++
	request := struct {
		Command   []any  `json:"command"`
		RequestID uint64 `json:"request_id"`
	}{command, p.nextID}
	deadline, _ := ctx.Deadline()
	if err := p.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = p.conn.SetWriteDeadline(time.Now()); close(interrupted) })
	err := json.NewEncoder(p.conn).Encode(request)
	if !stop() {
		<-interrupted
	}
	if err != nil {
		_ = p.conn.Close()
		return fmt.Errorf("write IPC: %w", err)
	}
	for {
		var r response
		select {
		case r = <-p.responses:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.readDone:
			select {
			case r = <-p.responses:
			default:
				return p.readErr
			}
		case <-p.done:
			return fmt.Errorf("mpv exited: %v", p.exitErr)
		}
		if r.RequestID != p.nextID {
			continue
		}
		if r.Error != "success" {
			return &commandError{message: r.Error}
		}
		if result != nil {
			return json.Unmarshal(r.Data, result)
		}
		return nil
	}
}

// Load accepts a local file only for this probe; it is not a Source contract.
func (p *Probe) Load(ctx context.Context, path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("audio path must be a regular file")
	}
	p.eventMu.Lock()
	p.playbackError = ""
	p.eventMu.Unlock()
	return p.call(ctx, []any{"loadfile", path, "replace"}, nil)
}
func (p *Probe) SetPaused(ctx context.Context, paused bool) error {
	return p.call(ctx, []any{"set_property", "pause", paused}, nil)
}
func (p *Probe) Stop(ctx context.Context) error { return p.call(ctx, []any{"stop"}, nil) }

// Status is a diagnostic snapshot, not authoritative daemon state.
type Status struct {
	Loading       bool     `json:"loading,omitempty"`
	Idle          bool     `json:"idle"`
	Paused        bool     `json:"paused"`
	Position      *float64 `json:"position_seconds,omitempty"`
	PlaybackError string   `json:"playback_error,omitempty"`
}

func (p *Probe) Status(ctx context.Context) (Status, error) {
	var s Status
	if err := p.call(ctx, []any{"get_property", "idle-active"}, &s.Idle); err != nil {
		return s, err
	}
	if err := p.call(ctx, []any{"get_property", "pause"}, &s.Paused); err != nil {
		return s, err
	}
	if !s.Idle {
		var position float64
		if err := p.call(ctx, []any{"get_property", "time-pos"}, &position); err != nil {
			var command *commandError
			if !errors.As(err, &command) || command.message != "property unavailable" {
				return s, err
			}
		} else {
			s.Position = &position
		}
	}
	p.eventMu.Lock()
	s.PlaybackError = p.playbackError
	s.Loading = p.loading
	p.eventMu.Unlock()
	return s, nil
}

// Close attempts graceful shutdown, then kills and reaps the owned child.
func (p *Probe) Close() error {
	p.closeOnce.Do(func() {
		if p.conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			_ = p.call(ctx, []any{"quit"}, nil)
			cancel()
			_ = p.conn.Close()
		}
		if p.cmd != nil {
			select {
			case <-p.done:
			case <-time.After(time.Second):
				_ = p.cmd.Process.Kill()
				<-p.done
			}
		}
		if p.conn != nil {
			<-p.readDone
		}
		p.closeErr = os.RemoveAll(p.dir)
	})
	return p.closeErr
}

// Done signals child exit, including unexpected termination while the CLI is idle.
func (p *Probe) Done() <-chan struct{} { return p.done }
