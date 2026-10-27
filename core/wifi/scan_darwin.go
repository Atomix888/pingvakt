package wifi

import "context"

func survey(ctx context.Context) ([]BSS, string, error) {
	out, err := run(ctx, "/usr/sbin/system_profiler", "SPAirPortDataType")
	if err != nil {
		return nil, "system_profiler", err
	}
	return parseSPNetworks(out), "system_profiler", nil
}
