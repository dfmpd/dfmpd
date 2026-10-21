package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// RPCError is a server-declared failure, distinct from a transport failure.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("RPC %d: %s", e.Code, e.Message) }

// Call opens one connection for one request. It never starts a daemon or retries
// mutations: a transport failure/timeout can leave the operation outcome unknown.
func Call(ctx context.Context, socket, method string, params any, result any) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	const id = "dfmpc-1"
	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxMessageBytes {
		return errors.New("request exceeds IPC size limit")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("connect to dfmpd: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("empty IPC write")
		}
		data = data[n:]
	}
	line, err := bufio.NewReaderSize(conn, MaxMessageBytes).ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("read IPC response: %w", err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil || fields == nil {
		return errors.New("invalid IPC response")
	}
	var version, replyID string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" || json.Unmarshal(fields["id"], &replyID) != nil || replyID != id {
		return errors.New("IPC response version or ID mismatch")
	}
	raw, hasResult := fields["result"]
	rawError, hasError := fields["error"]
	if hasResult == hasError {
		return errors.New("IPC response must contain exactly one result or error")
	}
	if hasError {
		var wire struct {
			Code    *int            `json:"code"`
			Message *string         `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(rawError, &wire) != nil || wire.Code == nil || wire.Message == nil {
			return errors.New("invalid IPC error")
		}
		return &RPCError{Code: *wire.Code, Message: *wire.Message, Data: wire.Data}
	}
	if result != nil {
		return json.Unmarshal(raw, result)
	}
	return nil
}
