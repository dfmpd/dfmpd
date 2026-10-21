package ipc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func runtimeDir(t *testing.T) string {
	t.Helper()
	// Keep socket paths below Unix sockaddr limits regardless of the test name.
	dir, err := os.MkdirTemp("", "dfmpd-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func TestSocketOwnership(t *testing.T) {
	dir := runtimeDir(t)
	socket, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	info, err := os.Stat(socket.Path())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
	if _, err := Listen(dir); err == nil {
		t.Fatal("duplicate listener accepted")
	}
	conn, err := net.Dial("unix", socket.Path())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket.Path()); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestUnsafeAndExistingPaths(t *testing.T) {
	if _, err := Listen(""); err == nil {
		t.Fatal("accepted unset runtime directory")
	}
	dir := runtimeDir(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(dir); err == nil {
		t.Fatal("accepted permissive directory")
	}
	os.Chmod(dir, 0700)
	parent := filepath.Join(dir, "dfmpd")
	os.Mkdir(parent, 0700)
	path := filepath.Join(parent, "control.sock")
	for _, kind := range []string{"file", "symlink", "stale socket"} {
		t.Run(kind, func(t *testing.T) {
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			case "stale socket":
				l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				l.SetUnlinkOnClose(false)
				l.Close()
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Listen(dir); err == nil {
				t.Fatal("existing path accepted")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("existing path modified")
			}
			os.Remove(path)
		})
	}
}
func TestCleanupDoesNotRemoveReplacement(t *testing.T) {
	s, err := Listen(runtimeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err == nil {
		t.Fatal("replacement not reported")
	}
	data, err := os.ReadFile(s.Path())
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement lost: %q %v", data, err)
	}
}
