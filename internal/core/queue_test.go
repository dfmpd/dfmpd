package core

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dot-1245/dfmpd/internal/media"
	"github.com/dot-1245/dfmpd/internal/playback"
	"github.com/dot-1245/dfmpd/internal/source"
)

type resolverFunc func(context.Context, string) (media.Descriptor, error)

func (f resolverFunc) Resolve(ctx context.Context, id string) (media.Descriptor, error) {
	return f(ctx, id)
}

type queuePlayer struct {
	state    playback.Status
	inputs   []string
	playFunc func(context.Context) error
}

func (p *queuePlayer) Play(ctx context.Context, input playback.Input) error {
	p.inputs = append(p.inputs, input.Location)
	if p.playFunc != nil {
		return p.playFunc(ctx)
	}
	p.state = playback.Status{State: playback.Playing}
	return nil
}
func (p *queuePlayer) Pause(context.Context) error {
	p.state.State = playback.Paused
	p.state.Paused = true
	return nil
}
func (p *queuePlayer) Resume(context.Context) error {
	p.state.State = playback.Playing
	p.state.Paused = false
	p.state.Idle = false
	return nil
}
func (p *queuePlayer) Stop(context.Context) error {
	p.state = playback.Status{State: playback.Idle, Idle: true}
	return nil
}
func (p *queuePlayer) Seek(context.Context, float64) error             { return nil }
func (p *queuePlayer) Status(context.Context) (playback.Status, error) { return p.state, nil }
func (p *queuePlayer) Close() error                                    { return nil }
func queueCall(t *testing.T, c *Core, cmd Command) QueueResult {
	t.Helper()
	out, err := c.Queue(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func queueKind(t *testing.T, err error, want string) {
	t.Helper()
	var e *QueueError
	if !errors.As(err, &e) || e.Kind != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}
func TestQueueLifecycle(t *testing.T) {
	p := &queuePlayer{}
	resolves := 0
	resolver := resolverFunc(func(_ context.Context, id string) (media.Descriptor, error) {
		resolves++
		if id == "bad" {
			return media.Descriptor{}, errors.New("secret URL")
		}
		return media.Descriptor{Location: "private-delivery-" + id}, nil
	})
	c := NewWithSources(context.Background(), p, map[string]source.Resolver{"test": resolver})
	defer c.Close()
	add := func(id string) string {
		return queueCall(t, c, Command{Operation: QueueAdd, Identity: media.Identity{SourceID: "test", ItemID: id}}).Entries[0].EntryID
	}
	first, duplicate, bad := add("same"), add("same"), add("bad")
	if first == duplicate || resolves != 0 {
		t.Fatal("duplicate identity lost or resolved during add")
	}
	page := queueCall(t, c, Command{Operation: QueueList, Offset: 1, Limit: 1})
	if page.Total != 3 || len(page.Entries) != 1 || page.Entries[0].EntryID != duplicate || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("page=%+v", page)
	}
	queueCall(t, c, Command{Operation: QueuePlay, EntryID: first})
	_, err := c.Queue(context.Background(), Command{Operation: QueuePlay, EntryID: bad})
	queueKind(t, err, "resolve_failed")
	page = queueCall(t, c, Command{Operation: QueueList, Limit: 20})
	if page.SelectedEntryID != first || len(p.inputs) != 1 || page.Outcome != "accepted" {
		t.Fatalf("resolve failure changed session: %+v", page)
	}
	queueCall(t, c, Command{Operation: QueueNext})
	if len(p.inputs) != 2 || resolves != 3 {
		t.Fatalf("resolve count=%d inputs=%v", resolves, p.inputs)
	}
	queueCall(t, c, Command{Operation: QueuePrevious})
	if resolves != 4 {
		t.Fatal("replay did not resolve afresh")
	}
	c.Execute(context.Background(), Command{Operation: Pause})
	_, err = c.Queue(context.Background(), Command{Operation: QueueNext})
	queueKind(t, err, "unsupported_state")
	if p.state.State != playback.Paused || len(p.inputs) != 3 {
		t.Fatal("next changed paused playback")
	}
	c.Execute(context.Background(), Command{Operation: Stop})
	_, err = c.Queue(context.Background(), Command{Operation: QueueNext})
	queueKind(t, err, "unsupported_state")
	_, err = c.Queue(context.Background(), Command{Operation: QueueRemove, EntryID: first})
	queueKind(t, err, "entry_in_use")
	queueCall(t, c, Command{Operation: QueueRemove, EntryID: duplicate})
	// Legacy direct play neither adds nor empties the queue and detaches selection.
	if _, err := c.Execute(context.Background(), Command{Operation: Play, Input: playback.Input{Location: "legacy"}}); err != nil {
		t.Fatal(err)
	}
	page = queueCall(t, c, Command{Operation: QueueList, Limit: 20})
	if page.Total != 2 || page.SelectedEntryID != "" || page.Outcome != "none" {
		t.Fatalf("legacy play: %+v", page)
	}
	queueCall(t, c, Command{Operation: QueueRemove, EntryID: first})
	if p.inputs[len(p.inputs)-1] != "legacy" {
		t.Fatal("remove stopped unrelated direct playback")
	}
}
func TestQueueUnknownOutcome(t *testing.T) {
	p := &queuePlayer{playFunc: func(ctx context.Context) error { <-ctx.Done(); return nil }} // backend reports late success
	resolver := resolverFunc(func(context.Context, string) (media.Descriptor, error) {
		return media.Descriptor{Location: "private"}, nil
	})
	c := NewWithSources(context.Background(), p, map[string]source.Resolver{"test": resolver})
	defer c.Close()
	id := queueCall(t, c, Command{Operation: QueueAdd, Identity: media.Identity{SourceID: "test", ItemID: "id"}}).Entries[0].EntryID
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Queue(ctx, Command{Operation: QueuePlay, EntryID: id}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	page := queueCall(t, c, Command{Operation: QueueList, Limit: 20})
	if page.Outcome != "unknown" || page.AttemptedEntryID != id || page.SelectedEntryID != "" {
		t.Fatalf("timeout falsely resolved: %+v", page)
	}
	_, err := c.Queue(context.Background(), Command{Operation: QueueNext})
	queueKind(t, err, "outcome_unknown")
	_, err = c.Queue(context.Background(), Command{Operation: QueueRemove, EntryID: id})
	queueKind(t, err, "entry_in_use")
	if _, err := c.Execute(context.Background(), Command{Operation: Stop}); err != nil {
		t.Fatal(err)
	}
	page = queueCall(t, c, Command{Operation: QueueList, Limit: 20})
	if page.Outcome != "none" || page.AttemptedEntryID != "" {
		t.Fatal("stop did not clear uncertainty")
	}
}
func TestConcurrentQueueAddsAndIsolation(t *testing.T) {
	c := NewWithSources(context.Background(), &queuePlayer{}, map[string]source.Resolver{"test": resolverFunc(func(context.Context, string) (media.Descriptor, error) { panic("must not resolve on add") })})
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Queue(context.Background(), Command{Operation: QueueAdd, Identity: media.Identity{SourceID: "test", ItemID: "same"}})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	page := queueCall(t, c, Command{Operation: QueueList, Limit: 100})
	ids := map[string]bool{}
	for _, entry := range page.Entries {
		if ids[entry.EntryID] {
			t.Fatal("duplicate entry ID")
		}
		ids[entry.EntryID] = true
	}
	if len(ids) != 30 {
		t.Fatal(len(ids))
	}
	original := page.Entries[0]
	page.Entries[0].Identity.ItemID = "changed"
	again := queueCall(t, c, Command{Operation: QueueList, Limit: 1})
	if !reflect.DeepEqual(again.Entries[0], original) {
		t.Fatal("snapshot aliases queue")
	}
	for _, cmd := range []Command{{Operation: QueueList, Offset: -1, Limit: 1}, {Operation: QueueList, Limit: 0}, {Operation: QueueList, Limit: 101}} {
		_, err := c.Queue(context.Background(), cmd)
		queueKind(t, err, "invalid_input")
	}
	page = queueCall(t, c, Command{Operation: QueueList, Offset: 100, Limit: 1})
	if len(page.Entries) != 0 || page.NextOffset != nil {
		t.Fatal("invalid end page")
	}
}
