// Package local resolves local file identities. It does not control Playback.
package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/dfmpd/dfmpd/internal/media"
)

const ID = "local"

type Resolver struct{}

// Identity interprets a user path in the caller's working directory. CLI callers
// use it before sending an identity to the daemon; Core never interprets paths.
func Identity(path string) (media.Identity, error) {
	if path == "" {
		return media.Identity{}, errors.New("empty local path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return media.Identity{}, err
	}
	return media.Identity{SourceID: ID, ItemID: absolute}, nil
}
func (Resolver) Resolve(ctx context.Context, itemID string) (media.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return media.Descriptor{}, err
	}
	if !filepath.IsAbs(itemID) {
		return media.Descriptor{}, errors.New("local identity must be an absolute path")
	}
	path := filepath.Clean(itemID)
	info, err := os.Stat(path)
	if err != nil {
		return media.Descriptor{}, err
	}
	if !info.Mode().IsRegular() {
		return media.Descriptor{}, errors.New("local identity must resolve to a regular file")
	}
	if err := ctx.Err(); err != nil {
		return media.Descriptor{}, err
	}
	return media.Descriptor{Location: path, Metadata: media.Metadata{Title: filepath.Base(path)}}, nil
}
