package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/dot-1245/dfmpd/internal/core"
	"github.com/dot-1245/dfmpd/internal/playback"
)

type fakePlayer struct {
	mu      sync.Mutex
	calls   int
	paused  bool
	err     error
	entered chan struct{}
	release chan struct{}
}

func (f *fakePlayer) Pause(ctx context.Context) error {
	f.mu.Lock()
	f.calls++
	f.paused = true
	err := f.err
	f.mu.Unlock()
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func (f *fakePlayer) Resume(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.paused = false
	return f.err
}
func (f *fakePlayer) Play(ctx context.Context, _ playback.Input) error { return f.Resume(ctx) }
func (f *fakePlayer) Stop(ctx context.Context) error                   { return f.Resume(ctx) }
func (f *fakePlayer) Seek(ctx context.Context, _ float64) error        { return f.Resume(ctx) }
func (f *fakePlayer) Status(context.Context) (playback.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return playback.Status{State: playback.Idle, Idle: true, Paused: f.paused}, f.err
}
func (f *fakePlayer) Close() error { return nil }

func TestProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		code, calls int
		silent      bool
	}{
		{name: "info", frame: `{"jsonrpc":"2.0","id":9007199254740993,"method":"daemon.info"}`},
		{name: "null id", frame: `{"jsonrpc":"2.0","id":null,"method":"daemon.info"}`},
		{name: "pause", frame: `{"jsonrpc":"2.0","id":"a","method":"playback.pause"}`, calls: 1},
		{name: "play", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.play","params":{"location":"/music/音声.flac"}}`, calls: 1},
		{name: "seek", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.seek","params":{"seconds":1.5}}`, calls: 1},
		{name: "notification", frame: `{"jsonrpc":"2.0","method":"playback.pause"}`, calls: 1, silent: true},
		{name: "unknown notification", frame: `{"jsonrpc":"2.0","method":"missing"}`, silent: true},
		{name: "invalid params notification", frame: `{"jsonrpc":"2.0","method":"playback.seek","params":{}}`, silent: true},
		{name: "malformed", frame: `{`, code: -32700},
		{name: "batch", frame: `[{"jsonrpc":"2.0","id":1,"method":"playback.pause"}]`, code: -32600},
		{name: "empty batch", frame: `[]`, code: -32600},
		{name: "null", frame: `null`, code: -32600},
		{name: "wrong version", frame: `{"jsonrpc":"1.0","id":1,"method":"daemon.info"}`, code: -32600},
		{name: "null method", frame: `{"jsonrpc":"2.0","id":1,"method":null}`, code: -32600},
		{name: "invalid id", frame: `{"jsonrpc":"2.0","id":true,"method":"daemon.info"}`, code: -32600},
		{name: "unknown", frame: `{"jsonrpc":"2.0","id":1,"method":"quit"}`, code: -32601},
		{name: "case sensitive params", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.play","params":{"Location":"/music/file.flac"}}`, code: -32602},
		{name: "relative path", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.play","params":{"location":"music.flac"}}`, code: -32602},
		{name: "negative seek", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.seek","params":{"seconds":-1}}`, code: -32602},
		{name: "missing seek", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.seek"}`, code: -32602},
		{name: "extra parameter", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.pause","params":{"extra":true}}`, code: -32602},
		{name: "positional params", frame: `{"jsonrpc":"2.0","id":1,"method":"playback.seek","params":[1]}`, code: -32602},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePlayer{}
			c := core.New(context.Background(), f)
			defer c.Close()
			out := handle(context.Background(), c, []byte(tc.frame))
			if (out == nil) != tc.silent {
				t.Fatalf("response=%+v", out)
			}
			if out != nil {
				if tc.code != 0 {
					if out.Error == nil || out.Error.Code != tc.code {
						t.Fatalf("response=%+v", out)
					}
				} else {
					if out.Error != nil {
						t.Fatal(out.Error)
					}
					var req map[string]json.RawMessage
					json.Unmarshal([]byte(tc.frame), &req)
					if string(out.ID) != string(req["id"]) {
						t.Fatalf("id changed: %s", out.ID)
					}
				}
			}
			f.mu.Lock()
			calls := f.calls
			f.mu.Unlock()
			if calls != tc.calls {
				t.Fatalf("calls=%d want=%d", calls, tc.calls)
			}
		})
	}
}
func TestOperationErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{context.DeadlineExceeded, -32001}, {context.Canceled, -32002},
		{&playback.Error{Code: playback.Unavailable, Message: "private diagnostic"}, -32003},
		{&playback.Error{Code: playback.InvalidInput}, -32602},
		{errors.New("unexpected"), -32603},
	} {
		out := operationError([]byte("1"), tc.err)
		if out.Error.Code != tc.code {
			t.Fatalf("%v: %+v", tc.err, out)
		}
	}
}
