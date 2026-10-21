package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "音声 file.flac")
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := Identity(path)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := (Resolver{}).Resolve(context.Background(), identity.ItemID)
	if err != nil || descriptor.Location != path || descriptor.Metadata.Title != "音声 file.flac" {
		t.Fatalf("descriptor=%+v err=%v", descriptor, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Resolver{}).Resolve(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{path, dir, "relative", link} {
		if _, err := (Resolver{}).Resolve(context.Background(), bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Resolver{}).Resolve(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	relative, err := Identity("relative.flac")
	if err != nil || !filepath.IsAbs(relative.ItemID) {
		t.Fatalf("identity=%+v err=%v", relative, err)
	}
}
