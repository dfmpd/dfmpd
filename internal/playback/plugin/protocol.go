// Package plugin implements the experimental Playback stdio protocol.
// It is independent of client IPC and is not a stable external plugin SDK.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/dfmpd/dfmpd/internal/playback"
)

const (
	Version         = 1
	MaxFrame        = 64 * 1024 // including LF
	StartupTimeout  = 5 * time.Second
	RPCTimeout      = 3 * time.Second
	ShutdownTimeout = 3 * time.Second
)

var operations = []string{"play", "pause", "resume", "stop", "seek", "status"}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	// Same-host PoC extension: a queued operation must not start after its
	// deadline. This is not a remote-clock or external SDK contract.
	Deadline int64 `json:"deadline_unix_ms"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    *playback.Error `json:"data,omitempty"`
}
type initialization struct {
	Version int `json:"protocol_version"`
}
type capabilities struct {
	Version    int      `json:"protocol_version"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Operations []string `json:"operations"`
	Inputs     []string `json:"inputs"`
}
type accepted struct {
	Accepted bool `json:"accepted"`
}

func (c capabilities) valid() bool {
	if c.Version != Version || c.Kind != "playback" || c.Name == "" || !slices.Contains(c.Inputs, "local-file") {
		return false
	}
	for _, op := range operations {
		if !slices.Contains(c.Operations, op) {
			return false
		}
	}
	return true
}
func decode(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null is not an object")
	}
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func frame(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxFrame {
		return nil, errors.New("plugin frame exceeds limit")
	}
	return append(b, '\n'), nil
}
func unavailable(message string) error {
	return &playback.Error{Code: playback.Unavailable, Message: message}
}

func validCode(code playback.Code) bool {
	switch code {
	case playback.InvalidInput, playback.Unavailable, playback.Rejected, playback.TimedOut, playback.Canceled, playback.Closed, playback.MediaFailed:
		return true
	}
	return false
}
