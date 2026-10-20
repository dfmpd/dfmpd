package mpv

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestIPC(t *testing.T) {
	for _, mode := range []string{"success", "error", "disconnect", "malformed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			client, server := net.Pipe()
			p := &Probe{conn: client, done: make(chan struct{}), responses: make(chan response, 1), readDone: make(chan struct{})}
			go p.read()
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				defer server.Close()
				var req struct {
					Command   []any  `json:"command"`
					RequestID uint64 `json:"request_id"`
				}
				if json.NewDecoder(server).Decode(&req) != nil {
					return
				}
				switch mode {
				case "success":
					_, _ = server.Write([]byte("{\"event\":\"file-loaded\"}\n"))
					_ = json.NewEncoder(server).Encode(response{RequestID: req.RequestID, Error: "success", Data: json.RawMessage(`true`)})
				case "error":
					_ = json.NewEncoder(server).Encode(response{RequestID: req.RequestID, Error: "property unavailable"})
				case "malformed":
					_, _ = server.Write([]byte("not json\n"))
				case "timeout":
					var b [1]byte
					_, _ = server.Read(b[:])
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			var value bool
			err := p.call(ctx, []any{"get_property", "pause"}, &value)
			if mode == "success" {
				if err != nil || !value {
					t.Fatalf("value=%v error=%v", value, err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			_ = client.Close()
			<-serverDone
			<-p.readDone
		})
	}
}

func TestStartFailureCleanup(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", base)
	if _, err := Start(context.Background(), filepath.Join(base, "missing-mpv")); err == nil {
		t.Fatal("expected startup error")
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatalf("leftovers=%v error=%v", entries, err)
	}
}

func TestRealMPVLifecycle(t *testing.T) {
	executable, err := exec.LookPath("mpv")
	if err != nil {
		t.Skip("mpv is not installed")
	}
	for _, crash := range []bool{false, true} {
		name := "normal"
		if crash {
			name = "crash"
		}
		t.Run(name, func(t *testing.T) {
			p, err := Start(context.Background(), executable)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			dir := p.dir
			if crash {
				if err := p.cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-p.done:
				case <-time.After(3 * time.Second):
					t.Fatal("child not reaped")
				}
				if _, err := p.Status(context.Background()); err == nil {
					t.Fatal("expected failure after crash")
				}
			} else {
				s, err := p.Status(context.Background())
				if err != nil || !s.Idle {
					t.Fatalf("status=%+v error=%v", s, err)
				}
				if err := p.SetPaused(context.Background(), true); err != nil {
					t.Fatal(err)
				}
				s, err = p.Status(context.Background())
				if err != nil || !s.Paused {
					t.Fatalf("status=%+v error=%v", s, err)
				}
				if err := p.SetPaused(context.Background(), false); err != nil {
					t.Fatal(err)
				}
				if err := p.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("socket directory remains: %v", err)
			}
		})
	}
}

// A generated PCM fixture and null output exercise decoding without an audio device.
func TestRealMPVPlayback(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv is not installed")
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "mpv-null")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec mpv --ao=null \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Ten seconds of silent, mono 8 kHz, 16-bit PCM.
	data := make([]byte, 44+160000)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], 160000)
	path := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), wrapper)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Load(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	wait := func(wantIdle, wantPaused bool) Status {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			s, err := p.Status(context.Background())
			if err == nil && s.Idle == wantIdle && s.Paused == wantPaused && (wantIdle || s.Position != nil) {
				return s
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("playback state did not converge")
		return Status{}
	}
	wait(false, false)
	if err := p.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	wait(false, true)
	if err := p.SetPaused(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	wait(false, false)
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	wait(true, false)
}
