package wifi

import (
	"context"
	"errors"
)

func collect(ctx context.Context) (*Info, error) {
	out, err := run(ctx, "netsh", "wlan", "show", "interfaces")
	if err != nil {
		return nil, err
	}
	if in := parseNetsh(out); in != nil {
		return in, nil
	}
	return nil, errors.New("no Wi-Fi connection")
}
