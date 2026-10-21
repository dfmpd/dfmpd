package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
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
)

func TestRealFLACQueue(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "builtin"
		if external {
			name = "external-plugin"
		}
		t.Run(name, func(t *testing.T) { testRealFLACQueue(t, external) })
	}
}
func testRealFLACQueue(t *testing.T, external bool) {
	for _, binary := range []string{"mpv", "ffmpeg"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " not installed")
		}
	}
	dir, err := os.MkdirTemp("", "dfmpd-flac-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	pluginPath := ""
	if external {
		binary := filepath.Join(dir, "plugin-bin")
		build := exec.Command("go", "build", "-race", "-o", binary, "../dfmpd-playback-mpv")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build plugin: %v %s", err, output)
		}
		pluginPath = filepath.Join(dir, "plugin")
		if err := os.WriteFile(pluginPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$XDG_RUNTIME_DIR/plugin.pid\"\nexec \"$XDG_RUNTIME_DIR/plugin-bin\" \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{filepath.Join(dir, "first.flac"), filepath.Join(dir, "second.flac")}
	for i, path := range paths {
		frequency := "sine=frequency=440:duration=15"
		if i == 1 {
			frequency = "sine=frequency=880:duration=15"
		}
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", frequency, "-c:a", "flac", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generate FLAC: %v %s", err, output)
		}
	}
	wrapper := filepath.Join(dir, "mpv-null")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$XDG_RUNTIME_DIR/mpv.pid\"\nexec mpv --ao=null \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWithPlugin(ctx, wrapper, pluginPath) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("daemon did not shut down")
		}
		pidFiles := []string{"mpv.pid"}
		if external {
			pidFiles = append(pidFiles, "plugin.pid")
		}
		for _, name := range pidFiles {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Error(err)
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Error(err)
				continue
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Errorf("%s process %d still exists: %v", name, pid, err)
			}
		}
		if sockets, _ := filepath.Glob(filepath.Join(dir, "dfmpd-*", "ipc")); len(sockets) != 0 {
			t.Errorf("mpv socket remains: %v", sockets)
		}
	}()
	socket := filepath.Join(dir, "dfmpd", "control.sock")
	deadline := time.Now().Add(6 * time.Second)
	for {
		err := ipc.Call(ctx, socket, "daemon.info", nil, nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	call := func(method string, params, result any) {
		t.Helper()
		if err := ipc.Call(ctx, socket, method, params, result); err != nil {
			t.Fatal(err)
		}
	}
	var entries []string
	for _, path := range paths {
		var page core.QueueResult
		call("queue.add", map[string]any{"identity": media.Identity{SourceID: "local", ItemID: path}}, &page)
		entries = append(entries, page.Entries[0].EntryID)
	}
	check := func(entryID, path string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			var status playback.Status
			call("playback.status", nil, &status)
			if status.State == playback.Playing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("not playing: %+v", status)
			}
			time.Sleep(20 * time.Millisecond)
		}
		var page core.QueueResult
		call("queue.list", nil, &page)
		if page.SelectedEntryID != entryID || page.Outcome != "accepted" {
			t.Fatalf("selection=%+v", page)
		}
		// Verify which file mpv actually loaded, not just the Core's accepted selection.
		sockets, _ := filepath.Glob(filepath.Join(dir, "dfmpd-*", "ipc"))
		if len(sockets) != 1 {
			t.Fatalf("mpv sockets=%v", sockets)
		}
		conn, err := net.Dial("unix", sockets[0])
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := json.NewEncoder(conn).Encode(map[string]any{"command": []string{"get_property", "path"}, "request_id": 1}); err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(conn)
		for {
			var reply struct {
				ID   int    `json:"request_id"`
				Data string `json:"data"`
			}
			if err := decoder.Decode(&reply); err != nil {
				t.Fatal(err)
			}
			if reply.ID == 1 {
				if reply.Data != path {
					t.Fatalf("loaded %q want %q", reply.Data, path)
				}
				break
			}
		}
	}
	call("queue.play", map[string]string{"entry_id": entries[0]}, nil)
	check(entries[0], paths[0])
	call("queue.next", nil, nil)
	check(entries[1], paths[1])
	call("queue.previous", nil, nil)
	check(entries[0], paths[0])
	call("playback.pause", nil, nil)
	if err := ipc.Call(ctx, socket, "queue.next", nil, nil); err == nil {
		t.Fatal("paused navigation accepted")
	}
	call("playback.seek", map[string]float64{"seconds": 4}, nil)
	call("playback.resume", nil, nil)
	check(entries[0], paths[0])
	var resumed playback.Status
	call("playback.status", nil, &resumed)
	if resumed.Paused || resumed.Position == nil || *resumed.Position < 3.5 {
		t.Fatalf("resume/seek: %+v", resumed)
	}
	call("playback.stop", nil, nil)
	if err := ipc.Call(ctx, socket, "queue.previous", nil, nil); err == nil {
		t.Fatal("stopped navigation accepted")
	}
}
