package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/ipc"
	"github.com/dfmpd/dfmpd/internal/media"
	"github.com/dfmpd/dfmpd/internal/playback"
)

func TestRealFLACQueue(t *testing.T) {
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
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec mpv --ao=null \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, wrapper) }()
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
	call("playback.stop", nil, nil)
	if err := ipc.Call(ctx, socket, "queue.previous", nil, nil); err == nil {
		t.Fatal("stopped navigation accepted")
	}
}
