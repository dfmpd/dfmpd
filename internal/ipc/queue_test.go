package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/dot-1245/dfmpd/internal/core"
	"github.com/dot-1245/dfmpd/internal/media"
	"github.com/dot-1245/dfmpd/internal/playback"
	"github.com/dot-1245/dfmpd/internal/source"
)

type queueResolver struct{}

func (queueResolver) Resolve(context.Context, string) (media.Descriptor, error) {
	return media.Descriptor{Location: "DELIVERYSECRET", Metadata: media.Metadata{Title: "private metadata"}}, nil
}

type queueFake struct{ fakePlayer }

func (f *queueFake) Status(context.Context) (playback.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := playback.Playing
	if f.paused {
		state = playback.Paused
	}
	return playback.Status{State: state, Paused: f.paused}, nil
}
func TestQueueWirePaginationAndPrivacy(t *testing.T) {
	c := core.NewWithSources(context.Background(), &queueFake{}, map[string]source.Resolver{"test": queueResolver{}})
	defer c.Close()
	for i := 0; i < 20; i++ {
		_, err := c.Queue(context.Background(), core.Command{Operation: core.QueueAdd, Identity: media.Identity{SourceID: "test", ItemID: strings.Repeat("<", core.MaxItemIDBytes)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	offset, totalSeen := 0, 0
	for {
		params, _ := json.Marshal(map[string]int{"offset": offset, "limit": 100})
		out := dispatchQueue(context.Background(), c, []byte(`"id"`), "queue.list", params)
		if out.Error != nil {
			t.Fatal(out.Error)
		}
		encoded, err := json.Marshal(out)
		if err != nil || len(encoded)+1 > MaxMessageBytes {
			t.Fatalf("size=%d err=%v", len(encoded), err)
		}
		page := out.Result.(core.QueueResult)
		if len(page.Entries) == 0 {
			t.Fatal("empty nonterminal page")
		}
		totalSeen += len(page.Entries)
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset {
			t.Fatal("pagination made no progress")
		}
		offset = *page.NextOffset
	}
	if totalSeen != 20 {
		t.Fatal(totalSeen)
	}
	if _, err := c.Queue(context.Background(), core.Command{Operation: core.QueuePlay, EntryID: "1"}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"queue.list", "playback.status"} {
		out := dispatch(context.Background(), c, []byte("1"), method, nil)
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "DELIVERYSECRET") || strings.Contains(string(encoded), "private metadata") {
			t.Fatalf("descriptor leaked: %s", encoded)
		}
	}
	out := dispatchQueue(context.Background(), c, []byte("1"), "queue.list", []byte(`{"limit":0}`))
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatal("invalid page accepted")
	}
	oversizedID, _ := json.Marshal(strings.Repeat("x", 1025))
	out = dispatchQueue(context.Background(), c, oversizedID, "queue.list", nil)
	if out.Error == nil || out.Error.Code != -32600 {
		t.Fatal("oversized queue id accepted")
	}
}
func TestQueueClientCoexistence(t *testing.T) {
	f := &queueFake{}
	path := startServer(t, f, map[string]source.Resolver{"test": queueResolver{}})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var result core.QueueResult
			err := Call(context.Background(), path, "queue.add", map[string]any{"identity": media.Identity{SourceID: "test", ItemID: "same"}}, &result)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var page core.QueueResult
	if err := Call(context.Background(), path, "queue.list", nil, &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 12 || len(page.Entries) != 12 {
		t.Fatalf("page=%+v", page)
	}
	ids := map[string]bool{}
	for _, entry := range page.Entries {
		if ids[entry.EntryID] {
			t.Fatal("duplicate ID")
		}
		ids[entry.EntryID] = true
	}
	call := func(method string, params any) {
		t.Helper()
		if err := Call(context.Background(), path, method, params, nil); err != nil {
			t.Fatal(err)
		}
	}
	call("queue.play", map[string]string{"entry_id": page.Entries[0].EntryID})
	call("queue.next", nil)
	call("queue.previous", nil)
	call("playback.pause", nil)
	if err := Call(context.Background(), path, "queue.next", nil, nil); err == nil {
		t.Fatal("paused next accepted")
	}
	call("playback.resume", nil)
	call("playback.play", map[string]string{"location": "/legacy/file.flac"})
	if err := Call(context.Background(), path, "queue.list", nil, &page); err != nil {
		t.Fatal(err)
	}
	if page.SelectedEntryID != "" || page.Total != 12 {
		t.Fatalf("legacy playback changed queue: %+v", page)
	}
	call("queue.remove", map[string]string{"entry_id": page.Entries[0].EntryID})
}
