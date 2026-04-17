package throttling

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

func IsStorageUnderPressure(ctx context.Context, client *redis.Client, maxUsage float64) (bool, error) {
	info, err := client.Info(ctx, "memory").Result()
	if err != nil {
		return false, err
	}

	var usedMemory, maxMemory float64

	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "used_memory:") {
			fmt.Sscanf(line, "used_memory:%f", &usedMemory)
		}
		if strings.HasPrefix(line, "maxmemory:") {
			fmt.Sscanf(line, "maxmemory:%f", &maxMemory)
		}
	}

	if maxMemory == 0 {
		return false, nil
	}

	usage := usedMemory / maxMemory
	return usage > maxUsage, nil
}