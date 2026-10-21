package core

import (
	"context"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/dfmpd/dfmpd/internal/media"
	"github.com/dfmpd/dfmpd/internal/playback"
	"github.com/dfmpd/dfmpd/internal/source"
)

const (
	QueueAdd         Operation = "queue.add"
	QueueList        Operation = "queue.list"
	QueueRemove      Operation = "queue.remove"
	QueuePlay        Operation = "queue.play"
	QueueNext        Operation = "queue.next"
	QueuePrevious    Operation = "queue.previous"
	MaxQueueEntries            = 1024
	MaxPageLimit               = 100
	DefaultPageLimit           = 20
	MaxItemIDBytes             = 2048
	MaxSourceIDBytes           = 64
)

type QueueError struct {
	Kind    string
	Message string
}

func (e *QueueError) Error() string         { return e.Kind + ": " + e.Message }
func queueError(kind, message string) error { return &QueueError{Kind: kind, Message: message} }

type Entry struct {
	EntryID  string         `json:"entry_id"`
	Identity media.Identity `json:"identity"`
}

// QueueResult contains identities only, never delivery locations or descriptors.
// Selection is the last accepted queue play, not proof of audible playback.
// An unknown outcome may have replaced it; only explicit successful play/stop
// establishes a new known outcome. Status cannot identify the loaded track.
type QueueResult struct {
	Entries          []Entry `json:"entries"`
	Total            int     `json:"total"`
	Offset           int     `json:"offset"`
	NextOffset       *int    `json:"next_offset"`
	Revision         uint64  `json:"revision"`
	SelectedEntryID  string  `json:"selected_entry_id,omitempty"`
	AttemptedEntryID string  `json:"attempted_entry_id,omitempty"`
	Outcome          string  `json:"outcome"` // none, accepted, unknown
}
type queueState struct {
	entries   []Entry
	nextID    uint64
	revision  uint64
	selected  string
	attempted string
	outcome   string
	sources   map[string]source.Resolver
}

func (q *queueState) snapshot(offset, limit int) QueueResult {
	total := len(q.entries)
	end := offset
	if offset < total {
		end = offset + limit
		if end > total {
			end = total
		}
	}
	entries := []Entry{}
	if offset < total {
		entries = append(entries, q.entries[offset:end]...)
	}
	out := QueueResult{Entries: entries, Total: total, Offset: offset, Revision: q.revision, SelectedEntryID: q.selected, AttemptedEntryID: q.attempted, Outcome: q.outcome}
	if end < total {
		out.NextOffset = &end
	}
	return out
}
func (q *queueState) index(id string) int {
	for i, e := range q.entries {
		if e.EntryID == id {
			return i
		}
	}
	return -1
}
func (q *queueState) uncertain(id string) { q.attempted = id; q.outcome = "unknown"; q.revision++ }
func (q *queueState) directPlay(ctx context.Context, a playback.Adapter, input playback.Input) error {
	q.uncertain("")
	err := a.Play(ctx, input)
	if err == nil && ctx.Err() == nil {
		q.selected = ""
		q.outcome = "none"
		q.revision++
	}
	if err == nil {
		err = ctx.Err()
	}
	return err
}
func (q *queueState) play(ctx context.Context, a playback.Adapter, index int) error {
	entry := q.entries[index]
	resolver := q.sources[entry.Identity.SourceID]
	if resolver == nil {
		return queueError("source_unavailable", "source is not configured")
	}
	descriptor, err := resolver.Resolve(ctx, entry.Identity.ItemID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Do not expose provider diagnostics that may contain delivery credentials.
		return queueError("resolve_failed", "source could not resolve the item")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// From this point even an error may follow partial backend execution.
	q.uncertain(entry.EntryID)
	err = a.Play(ctx, playback.Input{Location: descriptor.Location})
	if err == nil && ctx.Err() == nil {
		q.selected = entry.EntryID
		q.attempted = ""
		q.outcome = "accepted"
		q.revision++
	}
	if err == nil {
		err = ctx.Err()
	}
	return err
}
func (q *queueState) execute(ctx context.Context, a playback.Adapter, cmd Command) (QueueResult, error) {
	var err error
	switch cmd.Operation {
	case QueueAdd:
		id := cmd.Identity
		if id.SourceID == "" || id.ItemID == "" || len(id.SourceID) > MaxSourceIDBytes || len(id.ItemID) > MaxItemIDBytes || !utf8.ValidString(id.SourceID) || !utf8.ValidString(id.ItemID) {
			return QueueResult{}, queueError("invalid_input", "invalid or oversized media identity")
		}
		if q.sources[id.SourceID] == nil {
			return QueueResult{}, queueError("source_unavailable", "source is not configured")
		}
		if len(q.entries) >= MaxQueueEntries {
			return QueueResult{}, queueError("queue_full", "queue entry limit reached")
		}
		q.nextID++
		entry := Entry{EntryID: strconv.FormatUint(q.nextID, 10), Identity: id}
		q.entries = append(q.entries, entry)
		q.revision++
		return q.snapshot(len(q.entries)-1, 1), nil
	case QueueList:
		if cmd.Offset < 0 || cmd.Limit < 1 || cmd.Limit > MaxPageLimit {
			return QueueResult{}, queueError("invalid_input", "invalid queue page")
		}
		return q.snapshot(cmd.Offset, cmd.Limit), nil
	case QueueRemove:
		index := q.index(cmd.EntryID)
		if index < 0 {
			return QueueResult{}, queueError("not_found", "queue entry does not exist")
		}
		if cmd.EntryID == q.selected || cmd.EntryID == q.attempted {
			return QueueResult{}, queueError("entry_in_use", "select another item or use direct playback before removing this entry")
		}
		q.entries = append(q.entries[:index], q.entries[index+1:]...)
		q.revision++
	case QueuePlay:
		index := q.index(cmd.EntryID)
		if index < 0 {
			return QueueResult{}, queueError("not_found", "queue entry does not exist")
		}
		err = q.play(ctx, a, index)
	case QueueNext, QueuePrevious:
		if q.outcome == "unknown" {
			return QueueResult{}, queueError("outcome_unknown", "explicitly play a queue entry before navigating")
		}
		index := q.index(q.selected)
		if index < 0 {
			return QueueResult{}, queueError("no_selection", "no queue entry is selected")
		}
		if q.outcome != "accepted" {
			return QueueResult{}, queueError("unsupported_state", "next/previous requires an accepted playing queue selection")
		}
		status, e := a.Status(ctx)
		if e != nil {
			return QueueResult{}, e
		}
		if status.State != playback.Playing || status.Paused || status.Idle {
			return QueueResult{}, queueError("unsupported_state", "next/previous requires playing state in M3")
		}
		if cmd.Operation == QueueNext {
			index++
		} else {
			index--
		}
		if index < 0 || index >= len(q.entries) {
			return QueueResult{}, queueError("queue_boundary", "no adjacent entry")
		}
		err = q.play(ctx, a, index)
	default:
		err = fmt.Errorf("unknown queue operation: %s", cmd.Operation)
	}
	return q.snapshot(0, 0), err
}

// Queue submits queue operations to the same worker used by playback commands.
func (c *Core) Queue(ctx context.Context, command Command) (QueueResult, error) {
	out := c.submit(ctx, command)
	return out.queue, out.err
}
