//go:build linux

package plugin

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/ipc"
	"github.com/dfmpd/dfmpd/internal/media"
	"github.com/dfmpd/dfmpd/internal/playback"
	"github.com/dfmpd/dfmpd/internal/source"
	"github.com/dfmpd/dfmpd/internal/source/local"
)

func TestRealMPVFailureRecovery(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}
	binaryPath := os.Getenv("DFMPD_TEST_PLAYBACK_PLUGIN")
	if binaryPath == "" {
		binaryPath = filepath.Join(t.TempDir(), "dfmpd-playback-mpv")
		build := exec.Command("go", "build", "-race", "-o", binaryPath, "../../../cmd/dfmpd-playback-mpv")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build plugin: %v %s", err, output)
		}
	} else if !filepath.IsAbs(binaryPath) {
		t.Fatal("DFMPD_TEST_PLAYBACK_PLUGIN must be an absolute executable path")
	}
	for _, mode := range []string{"plugin-killed", "plugin-stopped"} {
		t.Run(mode, func(t *testing.T) {
			testSubreaper(t)
			dir, err := os.MkdirTemp("", "dfmpd-recovery-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			t.Setenv("XDG_RUNTIME_DIR", dir)
			wrapper := filepath.Join(dir, "mpv-null")
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s' \"$$\" > \"$XDG_RUNTIME_DIR/mpv.pid\"\nexec mpv --ao=null \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			// A minute of silence keeps playback active throughout the fault test.
			wav := make([]byte, 44+8000*2*60)
			copy(wav, "RIFF")
			binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
			copy(wav[8:], "WAVEfmt ")
			binary.LittleEndian.PutUint32(wav[16:], 16)
			binary.LittleEndian.PutUint16(wav[20:], 1)
			binary.LittleEndian.PutUint16(wav[22:], 1)
			binary.LittleEndian.PutUint32(wav[24:], 8000)
			binary.LittleEndian.PutUint32(wav[28:], 16000)
			binary.LittleEndian.PutUint16(wav[32:], 2)
			binary.LittleEndian.PutUint16(wav[34:], 16)
			copy(wav[36:], "data")
			binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))
			file := filepath.Join(dir, "audio.wav")
			if err := os.WriteFile(file, wav, 0600); err != nil {
				t.Fatal(err)
			}
			p, err := Open(context.Background(), binaryPath, "-mpv", wrapper)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			c := core.NewWithSources(ctx, p, map[string]source.Resolver{local.ID: local.Resolver{}})
			defer func() { cancel(); _ = c.Close() }()
			socket, err := ipc.Listen(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			served := make(chan error, 1)
			go func() { served <- ipc.Serve(ctx, socket.UnixListener, c) }()
			defer func() {
				cancel()
				if err := resultWithin(t, served); err != nil {
					t.Error(err)
				}
			}()
			call := func(method string, params, result any) error {
				return ipc.Call(context.Background(), socket.Path(), method, params, result)
			}
			var page core.QueueResult
			if err := call("queue.add", map[string]any{"identity": media.Identity{SourceID: local.ID, ItemID: file}}, &page); err != nil {
				t.Fatal(err)
			}
			entry := page.Entries[0].EntryID
			if err := call("queue.play", map[string]string{"entry_id": entry}, nil); err != nil {
				t.Fatal(err)
			}
			ready := time.NewTimer(3 * time.Second)
			defer ready.Stop()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				var s playback.Status
				if err := call("playback.status", nil, &s); err != nil {
					t.Fatal(err)
				}
				if s.State == playback.Playing {
					break
				}
				select {
				case <-ready.C:
					t.Fatal("mpv did not start playback")
				case <-tick.C:
				}
			}
			pidData, err := os.ReadFile(filepath.Join(dir, "mpv.pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "plugin-killed" {
				if err := p.cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-p.cleanupDone:
				case <-time.After(5 * time.Second):
					t.Fatal("plugin death not detected")
				}
			} else {
				if err := p.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
					t.Fatal(err)
				}
			}
			if err := call("playback.stop", nil, nil); err == nil {
				t.Fatal("failed stop reported success")
			}
			select {
			case <-p.cleanupDone:
			case <-time.After(5 * time.Second):
				t.Fatal("no automatic process recovery")
			}
			if err := call("daemon.info", nil, nil); err != nil {
				t.Fatalf("daemon unavailable: %v", err)
			}
			if err := call("queue.list", nil, &page); err != nil || page.Total != 1 || page.SelectedEntryID != entry || page.Outcome != "accepted" {
				t.Fatalf("queue changed after failed stop: %+v %v", page, err)
			}
			if err := call("playback.status", nil, nil); err == nil {
				t.Fatal("failed backend reported status success")
			}
			if err := call("queue.play", map[string]string{"entry_id": entry}, nil); err == nil {
				t.Fatal("dead backend accepted play")
			}
			if err := call("queue.list", nil, &page); err != nil || page.Total != 1 || page.Outcome != "unknown" {
				t.Fatalf("queue outcome: %+v %v", page, err)
			}
			// The test is a subreaper; real deployments delegate orphan reaping
			// to init/systemd. Wait verifies mpv actually terminated, not just
			// that a signal was sent or its parent disappeared.
			reaped := make(chan error, 1)
			go func() { var status syscall.WaitStatus; _, err := syscall.Wait4(pid, &status, 0, nil); reaped <- err }()
			if err := resultWithin(t, reaped); err != nil {
				t.Fatalf("mpv reap: %v", err)
			}
		})
	}
}
