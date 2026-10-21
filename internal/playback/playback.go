// Package playback defines the internal playback boundary, not a stable plugin ABI.
package playback

import "context"

// Input is a provisional playback location, not a media identity or Source
// descriptor. M1 supports local files only; other delivery methods remain open.
type Input struct {
	Location string `json:"location"`
}

type State string

const (
	Idle    State = "idle"
	Loading State = "loading"
	Playing State = "playing"
	Paused  State = "paused"
	Failed  State = "failed"
)

// Status describes observed backend state, not authoritative Core state.
// A nil position means it is not currently available; values are in seconds.
type Status struct {
	// Idle reports no active input; Paused records pause intent, even while idle.
	Idle     bool     `json:"idle"`
	Paused   bool     `json:"paused"`
	State    State    `json:"state"`
	Position *float64 `json:"position_seconds,omitempty"`
	Failure  *Error   `json:"failure,omitempty"`
}

type Code string

const (
	InvalidInput Code = "invalid_input"
	Unavailable  Code = "unavailable"
	Rejected     Code = "rejected"
	TimedOut     Code = "timed_out"
	Canceled     Code = "canceled"
	Closed       Code = "closed"
	MediaFailed  Code = "media_failed"
)

// Error contains portable diagnostics. No backend response or process handle
// crosses this boundary. Code, rather than Message, is for programmatic use.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }
func (e *Error) Is(target error) bool {
	if target == context.Canceled {
		return e.Code == Canceled
	}
	if target == context.DeadlineExceeded {
		return e.Code == TimedOut
	}
	other, ok := target.(*Error)
	return ok && other.Code == e.Code
}

// Adapter serializes operations and supports cancellation while waiting.
// Play replaces the current input and resumes; success means acceptance, not
// audible playback. Pause/Resume set intent even while idle. Seek is absolute,
// finite, nonnegative seconds; completion is observed with Status.
// Context/error are local Go call semantics; a future RPC proxy will translate
// deadlines/cancellation and Error values, not serialize Go contexts.
// Close is idempotent and owns backend cleanup.
type Adapter interface {
	Play(context.Context, Input) error
	Pause(context.Context) error
	Resume(context.Context) error
	Stop(context.Context) error
	Seek(context.Context, float64) error
	Status(context.Context) (Status, error)
	Close() error
}
