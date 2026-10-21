package mpv

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dfmpd/dfmpd/internal/playback"
)

func assertCode(t *testing.T, err error, code playback.Code) {
	t.Helper()
	var failure *playback.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestAdapterCommands(t *testing.T) {
	client, server := net.Pipe()
	p := &Probe{conn: client, done: make(chan struct{}), responses: make(chan response, 1), readDone: make(chan struct{})}
	a := &Adapter{probe: p, gate: make(chan struct{}, 1)}
	go p.read()
	t.Cleanup(func() { client.Close(); server.Close(); <-p.readDone })
	commands := make(chan []any, 8)
	go func() {
		decoder := json.NewDecoder(server)
		for {
			var request struct {
				Command []any  `json:"command"`
				ID      uint64 `json:"request_id"`
			}
			if decoder.Decode(&request) != nil {
				return
			}
			commands <- request.Command
			if json.NewEncoder(server).Encode(response{RequestID: request.ID, Error: "success"}) != nil {
				return
			}
		}
	}()
	path := filepath.Join(t.TempDir(), "音声 file.wav")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, err := range []error{a.Play(ctx, playback.Input{Location: path}), a.Pause(ctx), a.Resume(ctx), a.Seek(ctx, 2.5), a.Stop(ctx)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := [][]any{{"loadfile", path, "replace"}, {"set_property", "pause", false}, {"set_property", "pause", true}, {"set_property", "pause", false}, {"seek", 2.5, "absolute+exact"}, {"stop"}}
	for _, want := range expected {
		if got := <-commands; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	for _, seconds := range []float64{-1, math.NaN(), math.Inf(1)} {
		assertCode(t, a.Seek(ctx, seconds), playback.InvalidInput)
	}
	assertCode(t, a.Play(ctx, playback.Input{}), playback.InvalidInput)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err := a.Pause(canceled)
	assertCode(t, err, playback.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation identity lost")
	}
	// Cancellation also works while another operation owns the adapter.
	a.gate <- struct{}{}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	assertCode(t, a.Resume(deadline), playback.TimedOut)
	<-a.gate
}

func TestErrorTranslation(t *testing.T) {
	assertCode(t, translate(&commandError{message: "command failed"}), playback.Rejected)
	assertCode(t, translate(errors.New("disconnected")), playback.Unavailable)
	assertCode(t, translate(context.DeadlineExceeded), playback.TimedOut)
	if translate(nil) != nil {
		t.Fatal("nil error changed")
	}
}

func TestAdapterRealPlayback(t *testing.T) {
	path, wrapper := audioFixture(t)
	a, err := Open(context.Background(), wrapper)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	var player playback.Adapter = a
	ctx := context.Background()
	wait := func(state playback.State, at *float64) playback.Status {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		var last playback.Status
		for time.Now().Before(deadline) {
			s, err := player.Status(ctx)
			last = s
			if err != nil {
				t.Fatal(err)
			}
			if s.State == state && (at == nil || s.Position != nil && math.Abs(*s.Position-*at) < 0.35) {
				return s
			}
			time.Sleep(20 * time.Millisecond)
		}
		encoded, _ := json.Marshal(last)
		t.Fatalf("state did not converge to %s: %s", state, encoded)
		return playback.Status{}
	}
	wait(playback.Idle, nil)
	if err := player.Play(ctx, playback.Input{Location: path}); err != nil {
		t.Fatal(err)
	}
	wait(playback.Playing, nil)
	if err := player.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	wait(playback.Paused, nil)
	// Null output time-pos includes buffered audio; allow 350 ms while
	// still verifying forward and backward absolute seeks.
	for _, seconds := range []float64{4, 1} {
		if err := player.Seek(ctx, seconds); err != nil {
			t.Fatal(err)
		}
		wait(playback.Paused, &seconds)
	}
	if err := player.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	wait(playback.Playing, nil)
	if err := player.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	wait(playback.Idle, nil)
	// Play resets a prior pause and replaces the input.
	if err := player.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if err := player.Play(ctx, playback.Input{Location: path}); err != nil {
		t.Fatal(err)
	}
	wait(playback.Playing, nil)
	broken := filepath.Join(t.TempDir(), "broken.flac")
	if err := os.WriteFile(broken, []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := player.Play(ctx, playback.Input{Location: broken}); err != nil {
		t.Fatal(err)
	}
	s := wait(playback.Failed, nil)
	if s.Failure == nil || s.Failure.Code != playback.MediaFailed {
		t.Fatalf("missing media error: %+v", s)
	}
	if err := player.Play(ctx, playback.Input{Location: path}); err != nil {
		t.Fatal(err)
	}
	wait(playback.Playing, nil)
	// Transport death is an operation error, distinct from a media failure.
	if err := a.probe.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-a.Done()
	_, err = player.Status(ctx)
	assertCode(t, err, playback.Unavailable)
	if err := player.Close(); err != nil {
		t.Fatal(err)
	}
	if err := player.Close(); err != nil {
		t.Fatal(err)
	}
	assertCode(t, player.Resume(ctx), playback.Closed)
	if _, err := os.Stat(a.probe.dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestAdapterStatus(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		idle, paused, loading bool
		position              string
		failure               string
		state                 playback.State
	}{
		{name: "idle", idle: true, state: playback.Idle},
		{name: "loading", loading: true, position: "unavailable", state: playback.Loading},
		{name: "playing", position: "1.25", state: playback.Playing},
		{name: "paused", paused: true, position: "1.25", state: playback.Paused},
		{name: "failed", idle: true, failure: "decode failed", state: playback.Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			p := &Probe{conn: client, done: make(chan struct{}), responses: make(chan response, 1), readDone: make(chan struct{}), loading: tc.loading, playbackError: tc.failure}
			a := &Adapter{probe: p, gate: make(chan struct{}, 1)}
			go p.read()
			finished := make(chan struct{})
			t.Cleanup(func() { client.Close(); server.Close(); <-finished; <-p.readDone })
			go func() {
				defer close(finished)
				decoder := json.NewDecoder(server)
				for {
					var request struct {
						Command []string `json:"command"`
						ID      uint64   `json:"request_id"`
					}
					if decoder.Decode(&request) != nil {
						return
					}
					r := response{RequestID: request.ID, Error: "success"}
					switch request.Command[1] {
					case "idle-active":
						r.Data, _ = json.Marshal(tc.idle)
					case "pause":
						r.Data, _ = json.Marshal(tc.paused)
					case "time-pos":
						if tc.position == "unavailable" {
							r.Error = "property unavailable"
						} else {
							r.Data = json.RawMessage(tc.position)
						}
					}
					if json.NewEncoder(server).Encode(r) != nil {
						return
					}
				}
			}()
			s, err := a.Status(context.Background())
			if err != nil || s.State != tc.state {
				t.Fatalf("status=%+v error=%v", s, err)
			}
			if tc.failure != "" && (s.Failure == nil || s.Failure.Code != playback.MediaFailed) {
				t.Fatalf("missing failure: %+v", s)
			}
			encoded, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			var decoded playback.Status
			if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(s, decoded) {
				t.Fatalf("status is not portable: %s, %v", encoded, err)
			}
		})
	}
}
