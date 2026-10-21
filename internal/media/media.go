// Package media contains provisional internal media values, not a plugin schema.
package media

// Identity names an item within a configured Source instance. ItemID is opaque
// to Core. It is never a resolved delivery URL.
type Identity struct {
	SourceID string `json:"source_id"`
	ItemID   string `json:"item_id"`
}

type Metadata struct {
	Title           string   `json:"title,omitempty"`
	Artists         []string `json:"artists,omitempty"`
	Album           string   `json:"album,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
}

// Descriptor is transient and private to the Source -> Playback path. It must
// never be stored in a queue or serialized into public client state.
// M3 supports local delivery only; other delivery/authentication remains open.
type Descriptor struct {
	Location string
	Metadata Metadata
}
