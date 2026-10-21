// Package ipc implements DFMPD client IPC, independently of mpv JSON IPC.
package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path/filepath"

	"github.com/dfmpd/dfmpd/internal/core"
	"github.com/dfmpd/dfmpd/internal/playback"
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func failure(id json.RawMessage, code int, message string) *response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}
func validID(id json.RawMessage) bool {
	if bytes.Equal(id, []byte("null")) {
		return true
	}
	var s string
	if json.Unmarshal(id, &s) == nil {
		return true
	}
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return false
	}
	_, ok := v.(json.Number)
	return ok
}
func parameters(raw json.RawMessage, target any, names ...string) bool {
	if raw == nil {
		raw = []byte("{}")
	}
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for key := range fields {
		known := false
		for _, name := range names {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}

// handle never partially executes a batch. Valid notifications execute but do
// not receive responses, including on method/parameter errors.
func handle(ctx context.Context, c *core.Core, frame []byte) *response {
	frame = bytes.TrimSpace(frame)
	if !json.Valid(frame) {
		return failure(nil, -32700, "Parse error")
	}
	if len(frame) > 0 && frame[0] == '[' {
		return failure(nil, -32600, "Batch requests are not supported in M2")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(frame, &object) != nil || object == nil {
		return failure(nil, -32600, "Invalid Request")
	}
	var version, method string
	id, hasID := object["id"]
	if json.Unmarshal(object["jsonrpc"], &version) != nil || version != "2.0" || (len(object["method"]) == 0 || object["method"][0] != '"' || json.Unmarshal(object["method"], &method) != nil) || (hasID && !validID(id)) {
		return failure(nil, -32600, "Invalid Request")
	}
	if raw, ok := object["params"]; ok && (len(raw) == 0 || (raw[0] != '{' && raw[0] != '[')) {
		return failure(nil, -32600, "Invalid Request")
	}
	out := dispatch(ctx, c, id, method, object["params"])
	if !hasID {
		return nil
	}
	return out
}
func dispatch(ctx context.Context, c *core.Core, id json.RawMessage, method string, params json.RawMessage) *response {
	command := core.Command{}
	switch method {
	case "queue.add", "queue.list", "queue.remove", "queue.play", "queue.next", "queue.previous":
		return dispatchQueue(ctx, c, id, method, params)
	case "daemon.info":
		if !parameters(params, &struct{}{}) {
			return failure(id, -32602, "Invalid params")
		}
		return &response{JSONRPC: "2.0", ID: id, Result: struct {
			APIVersion int  `json:"api_version"`
			Batch      bool `json:"batch"`
			Queue      bool `json:"queue"`
		}{1, false, true}}
	case "playback.play":
		var p struct {
			Location string `json:"location"`
		}
		if !parameters(params, &p, "location") || !filepath.IsAbs(p.Location) {
			return failure(id, -32602, "location must be an absolute local path")
		}
		command.Operation, command.Input = core.Play, playback.Input{Location: p.Location}
	case "playback.seek":
		var p struct {
			Seconds *float64 `json:"seconds"`
		}
		if !parameters(params, &p, "seconds") || p.Seconds == nil || *p.Seconds < 0 || math.IsInf(*p.Seconds, 0) || math.IsNaN(*p.Seconds) {
			return failure(id, -32602, "seconds must be finite and nonnegative")
		}
		command.Operation, command.Seconds = core.Seek, *p.Seconds
	case "playback.pause", "playback.resume", "playback.stop", "playback.status":
		if !parameters(params, &struct{}{}) {
			return failure(id, -32602, "Invalid params")
		}
		switch method {
		case "playback.pause":
			command.Operation = core.Pause
		case "playback.resume":
			command.Operation = core.Resume
		case "playback.stop":
			command.Operation = core.Stop
		case "playback.status":
			command.Operation = core.Status
		}
	default:
		return failure(id, -32601, "Method not found")
	}
	status, err := c.Execute(ctx, command)
	if err != nil {
		return operationError(id, err)
	}
	var result any = struct {
		Accepted bool `json:"accepted"`
	}{true}
	if command.Operation == core.Status {
		result = statusDTO(status)
	}
	return &response{JSONRPC: "2.0", ID: id, Result: result}
}

// Keep the client schema explicit rather than exposing adapter objects.
type errorData struct {
	Kind string `json:"kind"`
}
type mediaFailure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}
type statusResult struct {
	State    string        `json:"state"`
	Idle     bool          `json:"idle"`
	Paused   bool          `json:"paused"`
	Position *float64      `json:"position_seconds,omitempty"`
	Failure  *mediaFailure `json:"failure,omitempty"`
}

func statusDTO(s playback.Status) statusResult {
	out := statusResult{State: string(s.State), Idle: s.Idle, Paused: s.Paused, Position: s.Position}
	if s.Failure != nil {
		out.Failure = &mediaFailure{Kind: string(s.Failure.Code), Message: s.Failure.Message}
	}
	return out
}
func operationError(id json.RawMessage, err error) *response {
	kind, code, message := "internal", -32603, "Internal error"
	var e *playback.Error
	var queueErr *core.QueueError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind, code, message = "timed_out", -32001, "Request timed out; outcome may be unknown"
	case errors.Is(err, context.Canceled):
		kind, code, message = "canceled", -32002, "Request canceled; outcome may be unknown"
	case errors.As(err, &queueErr):
		kind, code, message = queueErr.Kind, -32010, queueErr.Message
		if queueErr.Kind == "invalid_input" {
			code = -32602
		}
	case errors.As(err, &e):
		kind, code, message = string(e.Code), -32000, "Playback operation failed"
		switch e.Code {
		case playback.InvalidInput:
			code, message = -32602, "Invalid playback input"
		case playback.Unavailable:
			code, message = -32003, "Playback backend unavailable"
		case playback.Closed:
			code, message = -32004, "Playback session closed"
		case playback.Rejected:
			code, message = -32005, "Playback operation rejected"
		}
	}
	out := failure(id, code, message)
	out.Error.Data = errorData{Kind: kind}
	return out
}
