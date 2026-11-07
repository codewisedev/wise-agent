package modelgateway

import (
	"context"
	"time"
)

// SetSleepForTest replaces the retry backoff sleep.
func SetSleepForTest(g *Gateway, f func(context.Context, time.Duration) error) { g.sleep = f }

// SetNowForTest replaces the gateway clock.
func SetNowForTest(g *Gateway, f func() time.Time) { g.now = f }
