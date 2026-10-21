package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dfmpd/dfmpd/internal/playback"
)

type fakeAdapter struct {
	entered chan struct{}
	release chan struct{}
	active  atomic.Int32
	overlap atomic.Bool
	calls   atomic.Int32
	closes  atomic.Int32
}

func (f *fakeAdapter) Pause(ctx context.Context) error {
	if f.active.Add(1) != 1 {
		f.overlap.Store(true)
	}
	defer f.active.Add(-1)
	f.calls.Add(1)
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
	return nil
}
func (f *fakeAdapter) Play(ctx context.Context, _ playback.Input) error { return f.Pause(ctx) }
func (f *fakeAdapter) Resume(ctx context.Context) error                 { return f.Pause(ctx) }
func (f *fakeAdapter) Stop(ctx context.Context) error                   { return f.Pause(ctx) }
func (f *fakeAdapter) Seek(ctx context.Context, _ float64) error        { return f.Pause(ctx) }
func (f *fakeAdapter) Status(ctx context.Context) (playback.Status, error) {
	return playback.Status{State: playback.Idle, Idle: true}, f.Pause(ctx)
}
func (f *fakeAdapter) Close() error { f.closes.Add(1); return nil }

func TestSerializationAndFreshStatus(t *testing.T) {
	f := &fakeAdapter{}
	c := New(context.Background(), f)
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Execute(context.Background(), Command{Operation: Status}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.overlap.Load() || f.calls.Load() != 20 {
		t.Fatalf("overlap=%v calls=%d", f.overlap.Load(), f.calls.Load())
	}
}
func TestWaitingCancellationAndShutdown(t *testing.T) {
	f := &fakeAdapter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	c := New(context.Background(), f)
	defer c.Close()
	first := make(chan error, 1)
	go func() { _, err := c.Execute(context.Background(), Command{Operation: Pause}); first <- err }()
	<-f.entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Execute(ctx, Command{Operation: Stop}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting request: %v", err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("canceled waiting request reached adapter")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown hung")
	}
	if err := c.Close(); err != nil || f.closes.Load() != 1 {
		t.Fatalf("close=%v calls=%d", err, f.closes.Load())
	}
	if _, err := c.Execute(context.Background(), Command{Operation: Pause}); err == nil {
		t.Fatal("accepted after close")
	}
}
