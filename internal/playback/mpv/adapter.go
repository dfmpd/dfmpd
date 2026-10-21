package mpv

import (
	"context"
	"errors"
	"math"
	"net"
	"os"
	"time"

	"github.com/dfmpd/dfmpd/internal/playback"
)

// Adapter wraps the M0 transport. Construction/lifecycle stay backend-specific.
// Only the playback.Adapter interface is needed by playback consumers.
type Adapter struct {
	probe    *Probe
	gate     chan struct{}
	closed   bool // guarded by gate
	closeErr error
}

var _ playback.Adapter = (*Adapter)(nil)

func Open(ctx context.Context, executable string) (*Adapter, error) {
	p, err := Start(ctx, executable)
	if err != nil {
		return nil, translate(err)
	}
	return &Adapter{probe: p, gate: make(chan struct{}, 1)}, nil
}

func translate(err error) error {
	if err == nil {
		return nil
	}
	code := playback.Unavailable
	var command *commandError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		code = playback.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = playback.TimedOut
	case errors.As(err, &network) && network.Timeout():
		code = playback.TimedOut
	case errors.As(err, &command):
		code = playback.Rejected
	}
	return &playback.Error{Code: code, Message: err.Error()}
}

func (a *Adapter) run(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case a.gate <- struct{}{}:
	case <-ctx.Done():
		return translate(ctx.Err())
	}
	defer func() { <-a.gate }()
	if a.closed {
		return &playback.Error{Code: playback.Closed, Message: "playback adapter is closed"}
	}
	if err := ctx.Err(); err != nil {
		return translate(err)
	}
	return translate(fn(ctx))
}

func (a *Adapter) Play(ctx context.Context, input playback.Input) error {
	if input.Location == "" {
		return &playback.Error{Code: playback.InvalidInput, Message: "empty playback location"}
	}
	info, err := os.Stat(input.Location)
	if err != nil || !info.Mode().IsRegular() {
		return &playback.Error{Code: playback.InvalidInput, Message: "location must be an accessible regular local file"}
	}
	return a.run(ctx, func(ctx context.Context) error {
		if err := a.probe.Load(ctx, input.Location); err != nil {
			return err
		}
		return a.probe.SetPaused(ctx, false)
	})
}
func (a *Adapter) Pause(ctx context.Context) error {
	return a.run(ctx, func(ctx context.Context) error { return a.probe.SetPaused(ctx, true) })
}
func (a *Adapter) Resume(ctx context.Context) error {
	return a.run(ctx, func(ctx context.Context) error { return a.probe.SetPaused(ctx, false) })
}
func (a *Adapter) Stop(ctx context.Context) error { return a.run(ctx, a.probe.Stop) }
func (a *Adapter) Seek(ctx context.Context, seconds float64) error {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return &playback.Error{Code: playback.InvalidInput, Message: "seek requires finite, nonnegative seconds"}
	}
	return a.run(ctx, func(ctx context.Context) error {
		return a.probe.call(ctx, []any{"seek", seconds, "absolute+exact"}, nil)
	})
}
func (a *Adapter) Status(ctx context.Context) (playback.Status, error) {
	s := playback.Status{}
	err := a.run(ctx, func(ctx context.Context) error {
		legacy, err := a.probe.Status(ctx)
		if err != nil {
			return err
		}
		s.Idle, s.Paused = legacy.Idle, legacy.Paused
		s.Position = legacy.Position
		switch {
		case legacy.PlaybackError != "":
			s.State = playback.Failed
			s.Failure = &playback.Error{Code: playback.MediaFailed, Message: legacy.PlaybackError}
		case legacy.Loading:
			s.State = playback.Loading
		case legacy.Idle:
			s.State = playback.Idle
		case legacy.Position == nil:
			s.State = playback.Loading
		case legacy.Paused:
			s.State = playback.Paused
		default:
			s.State = playback.Playing
		}
		return nil
	})
	return s, err
}
func (a *Adapter) Close() error {
	a.gate <- struct{}{}
	defer func() { <-a.gate }()
	if !a.closed {
		a.closed = true
		a.closeErr = translate(a.probe.Close())
	}
	return a.closeErr
}

// Done is a local diagnostic for mpv-probe, deliberately absent from the
// backend-independent interface. Future plugin lifecycle belongs to its host.
func (a *Adapter) Done() <-chan struct{} { return a.probe.Done() }
