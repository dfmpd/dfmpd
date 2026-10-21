package plugin

import (
	"errors"
	"time"
)

const (
	terminationGrace = time.Second
	reapTimeout      = time.Second
)

// supervise is the sole owner of group signaling and cmd.Wait. No group signal
// may follow Wait, since the OS may then reuse the group leader's PID.
// It runs independently of Core, RPC callers, and stdout EOF.
func (p *Proxy) supervise() {
	defer close(p.cleanupDone)
	exited := make(chan error, 1)
	go func() { exited <- observeExit(p.cmd) }()
	var exitErr error
	observed := false
	select {
	case exitErr = <-exited:
		observed = true
	case <-p.broken:
		p.cleanupErr = signalGroup(p.cmd, false)
		select {
		case exitErr = <-exited:
			observed = true
		case <-time.After(terminationGrace):
		}
	}
	// Even a cleanly exited leader may have left descendants running or holding
	// stdout open. Kill the remaining group before reaping the leader.
	if !observed || exitErr == nil {
		p.cleanupErr = errors.Join(p.cleanupErr, signalGroup(p.cmd, true))
	} else {
		// An unexpected waitid failure means ownership could not be established.
		// Do not risk signaling an unowned/reused process group.
		p.cleanupErr = errors.Join(p.cleanupErr, exitErr)
		_ = p.cmd.Process.Kill() // direct child handle, never a possibly reused PGID
	}
	if !observed {
		select {
		case exitErr = <-exited:
		case <-time.After(reapTimeout):
			p.cleanupErr = errors.Join(p.cleanupErr, unavailable("plugin reap deadline exceeded"))
			p.fail(unavailable("plugin process did not exit after SIGKILL"))
			// Uninterruptible kernel sleep can outlast any deadline. Eventually
			// reap the child without blocking daemon shutdown or signaling again.
			go func() { <-exited; p.reap() }()
			return
		}
	}
	p.cleanupErr = errors.Join(p.cleanupErr, exitErr)
	go p.reap()
	select {
	case <-p.wait:
	case <-time.After(reapTimeout):
		p.cleanupErr = errors.Join(p.cleanupErr, unavailable("plugin Wait deadline exceeded"))
		p.fail(unavailable("plugin reaping did not complete"))
		return
	}
	// Drain the final shutdown response before marking the stream unavailable.
	// This is bounded even if a descendant escaped the group with stdout open.
	select {
	case <-p.readerDone:
	case <-time.After(reapTimeout):
	}
	p.fail(unavailable("playback plugin exited"))
}

func (p *Proxy) reap() {
	p.waitErr = p.cmd.Wait()
	close(p.wait)
}
