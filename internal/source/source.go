// Package source defines only the resolution capability needed by M3.
package source

import (
	"context"
	"github.com/dfmpd/dfmpd/internal/media"
)

type Resolver interface {
	Resolve(context.Context, string) (media.Descriptor, error)
}
