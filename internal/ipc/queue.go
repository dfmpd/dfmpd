package ipc

import (
	"context"
	"encoding/json"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/media"
)

func dispatchQueue(ctx context.Context, c *core.Core, id json.RawMessage, method string, params json.RawMessage) *response {
	// Leave room for at least one maximum-sized identity after JSON escaping.
	if len(id) > 1024 {
		return failure(nil, -32600, "Queue request ID exceeds 1024 bytes")
	}
	command := core.Command{Operation: core.Operation(method)}
	switch method {
	case "queue.add":
		var p struct {
			Identity json.RawMessage `json:"identity"`
		}
		if !parameters(params, &p, "identity") {
			return failure(id, -32602, "Invalid params")
		}
		var identity media.Identity
		if !parameters(p.Identity, &identity, "source_id", "item_id") {
			return failure(id, -32602, "Invalid identity")
		}
		command.Identity = identity
	case "queue.list":
		var p struct {
			Offset int  `json:"offset"`
			Limit  *int `json:"limit"`
		}
		if !parameters(params, &p, "offset", "limit") {
			return failure(id, -32602, "Invalid params")
		}
		command.Offset, command.Limit = p.Offset, core.DefaultPageLimit
		if p.Limit != nil {
			command.Limit = *p.Limit
		}
	case "queue.remove", "queue.play":
		var p struct {
			EntryID string `json:"entry_id"`
		}
		if !parameters(params, &p, "entry_id") || p.EntryID == "" {
			return failure(id, -32602, "entry_id is required")
		}
		command.EntryID = p.EntryID
	case "queue.next", "queue.previous":
		if !parameters(params, &struct{}{}) {
			return failure(id, -32602, "Invalid params")
		}
	default:
		return failure(id, -32601, "Method not found")
	}
	page, err := c.Queue(ctx, command)
	if err != nil {
		return operationError(id, err)
	}
	out := &response{JSONRPC: "2.0", ID: id, Result: page}
	if method == "queue.list" {
		// limit is a maximum, not a promise: shorten a page to fit the wire budget.
		for {
			data, err := json.Marshal(out)
			if err != nil {
				return failure(id, -32603, "Internal error")
			}
			if len(data)+1 <= MaxMessageBytes {
				break
			}
			if len(page.Entries) <= 1 {
				return failure(id, -32603, "Queue entry exceeds response budget")
			}
			page.Entries = page.Entries[:len(page.Entries)-1]
			next := page.Offset + len(page.Entries)
			page.NextOffset = &next
			out.Result = page
		}
	}
	return out
}
