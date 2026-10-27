//go:build !linux && !windows && !darwin

package wifi

import (
	"context"
	"errors"
)

func survey(ctx context.Context) ([]BSS, string, error) {
	return nil, "", errors.New("Wi-Fi scanning is not supported on this system")
}
