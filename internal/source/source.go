// Package source defines only the resolution capability needed by M3.
package source

import (
	"context"
	"github.com/dot-1245/dfmpd/internal/media"
)

type Resolver interface {
	Resolve(context.Context, string) (media.Descriptor, error)
}
