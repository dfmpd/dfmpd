// Package plugin owns the daemon-side Playback proxy and process supervision.
// The independent SDK owns the experimental wire contract and stdio server.
package plugin

import (
	sdk "github.com/dfmpd/dfmpd-sdk/playback"
	"github.com/dfmpd/dfmpd/internal/playback"
)

// Aliases keep wire values internal here without duplicating their definitions.
const (
	Version         = sdk.Version
	MaxFrame        = sdk.MaxFrame
	StartupTimeout  = sdk.StartupTimeout
	RPCTimeout      = sdk.RPCTimeout
	ShutdownTimeout = sdk.ShutdownTimeout
)

type request = sdk.Request
type response = sdk.Response
type initialization = sdk.Initialization
type capabilities = sdk.Capabilities
type accepted = sdk.Accepted

func unavailable(message string) error {
	return &playback.Error{Code: playback.Unavailable, Message: message}
}
