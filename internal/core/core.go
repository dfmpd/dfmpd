// Package core owns one playback session and serializes access to its adapter.
package core

import (
	"context"
	"time"

	"github.com/dfmpd/dfmpd/internal/media"
	"github.com/dfmpd/dfmpd/internal/playback"
	"github.com/dfmpd/dfmpd/internal/source"
)

const RequestTimeout = 3 * time.Second

type Operation string

const (
	Play   Operation = "play"
	Pause  Operation = "pause"
	Resume Operation = "resume"
	Stop   Operation = "stop"
	Seek   Operation = "seek"
	Status Operation = "status"
)

type Command struct {
	Operation Operation
	Input     playback.Input
	Seconds   float64
	Identity  media.Identity
	EntryID   string
	Offset    int
	Limit     int
}
type result struct {
	queue  QueueResult
	status playback.Status
	err    error
}
type request struct {
	ctx     context.Context
	command Command
	reply   chan result
}

// Core takes exclusive ownership of the adapter. It never caches playback state.
// Requests are executed in acceptance order; simultaneous senders have no fixed
// precedence. Close cancels work, then closes the adapter exactly once.
type Core struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests chan request
	done     chan struct{}
	closeErr error // published by closing done
}

func New(parent context.Context, adapter playback.Adapter) *Core {
	return NewWithSources(parent, adapter, nil)
}

func NewWithSources(parent context.Context, adapter playback.Adapter, sources map[string]source.Resolver) *Core {
	resolvers := make(map[string]source.Resolver, len(sources))
	for id, resolver := range sources {
		resolvers[id] = resolver
	}
	ctx, cancel := context.WithCancel(parent)
	c := &Core{ctx: ctx, cancel: cancel, requests: make(chan request), done: make(chan struct{})}
	go c.run(adapter, &queueState{sources: resolvers, outcome: "none"})
	return c
}
func (c *Core) Execute(ctx context.Context, command Command) (playback.Status, error) {
	out := c.submit(ctx, command)
	return out.status, out.err
}
func (c *Core) submit(ctx context.Context, command Command) result {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	r := request{ctx: ctx, command: command, reply: make(chan result, 1)}
	select {
	case <-c.ctx.Done():
		return result{err: &playback.Error{Code: playback.Closed, Message: "core is shutting down"}}
	case <-ctx.Done():
		return result{err: ctx.Err()}
	case c.requests <- r:
	}
	select {
	case reply := <-r.reply:
		return reply
	case <-ctx.Done():
		return result{err: ctx.Err()}
	}
}
func (c *Core) run(adapter playback.Adapter, q *queueState) {
	defer func() { c.closeErr = adapter.Close(); close(c.done) }()
	for {
		select {
		case <-c.ctx.Done():
			return
		case r := <-c.requests:
			if c.ctx.Err() != nil {
				return
			}
			if err := r.ctx.Err(); err != nil {
				r.reply <- result{err: err}
				continue
			}
			var out result
			switch r.command.Operation {
			case Play:
				out.err = q.directPlay(r.ctx, adapter, r.command.Input)
			case Pause:
				out.err = adapter.Pause(r.ctx)
			case Resume:
				out.err = adapter.Resume(r.ctx)
			case Stop:
				out.err = adapter.Stop(r.ctx)
				if out.err == nil && r.ctx.Err() == nil {
					q.attempted = ""
					q.outcome = "none"
					q.revision++
				}
			case Seek:
				out.err = adapter.Seek(r.ctx, r.command.Seconds)
			case Status:
				out.status, out.err = adapter.Status(r.ctx)
			case QueueAdd, QueueList, QueueRemove, QueuePlay, QueueNext, QueuePrevious:
				out.queue, out.err = q.execute(r.ctx, adapter, r.command)
			default:
				out.err = &playback.Error{Code: playback.InvalidInput, Message: "unknown playback operation"}
			}
			r.reply <- out
		}
	}
}
func (c *Core) Close() error { c.cancel(); <-c.done; return c.closeErr }
