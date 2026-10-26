//go:build !linux && !windows && !darwin

package wifi

import (
	"context"
	"errors"
)

func collect(context.Context) (*Info, error) { return nil, errors.New("stöds inte") }
