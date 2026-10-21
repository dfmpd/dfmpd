package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCommands(t *testing.T) {
	cases := []struct {
		args   []string
		method string
	}{
		{[]string{"info"}, "daemon.info"}, {[]string{"status"}, "playback.status"},
		{[]string{"pause"}, "playback.pause"}, {[]string{"resume"}, "playback.resume"}, {[]string{"stop"}, "playback.stop"},
		{[]string{"next"}, "queue.next"}, {[]string{"previous"}, "queue.previous"},
		{[]string{"seek", "1.5"}, "playback.seek"}, {[]string{"play", "音声.flac"}, "playback.play"},
		{[]string{"queue", "add", "音声.flac"}, "queue.add"}, {[]string{"queue", "list", "--offset", "2", "--limit", "3"}, "queue.list"},
		{[]string{"queue", "play", "2"}, "queue.play"}, {[]string{"queue", "remove", "2"}, "queue.remove"},
	}
	for _, tc := range cases {
		method, params, err := command(tc.args)
		if err != nil || method != tc.method {
			t.Fatalf("%v: %s %v", tc.args, method, err)
		}
		if method == "queue.add" || method == "playback.play" {
			encoded, _ := json.Marshal(params)
			abs, _ := filepath.Abs("音声.flac")
			if !bytes.Contains(encoded, []byte(abs)) {
				t.Fatalf("relative path sent: %s", encoded)
			}
		}
	}
	for _, args := range [][]string{{}, {"seek", "NaN"}, {"seek", "-1"}, {"queue", "list", "--limit", "101"}, {"queue", "list", "--offset", "-1"}, {"start"}, {"play"}, {"pause", "extra"}} {
		if _, _, err := command(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestNoDaemonStartup(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "missing.sock")
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"--socket", socket, "status"}, &out, &stderr)
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("client created socket")
	}
	if out.Len() != 0 {
		t.Fatal("unexpected stdout")
	}
}
