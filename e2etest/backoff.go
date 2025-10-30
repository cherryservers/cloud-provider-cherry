package e2etest

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

type backoffStoppedError struct{}

func (e backoffStoppedError) Error() string {
	return "exp backoff stopped"
}

type ExpBackoffConfig struct {
	start time.Duration
	exp   int
	max   time.Duration
}

func defaultExpBackoffConfig() ExpBackoffConfig {
	return ExpBackoffConfig{
		start: 2 * time.Second,
		exp:   2,
		max:   32 * time.Second,
	}
}

// expBackoff retries f until it returns true, a non-nil error or
// stop returns true.
func expBackoff(f func() (bool, error), cfg ExpBackoffConfig, stop func() bool) error {
	dur := cfg.start
	for !stop() {
		r, err := f()
		if err != nil || r {
			return err
		}
		time.Sleep(dur + time.Duration(rand.Intn(100)+100)*time.Millisecond)
		dur *= time.Duration(cfg.exp)
		if dur > cfg.max {
			dur = cfg.max
		}
	}
	return backoffStoppedError{}
}

type ExpBackoffConfigWithContext struct {
	ExpBackoffConfig
	ctx context.Context
}

func defaultExpBackoffConfigWithContext(ctx context.Context) ExpBackoffConfigWithContext {
	return ExpBackoffConfigWithContext{ExpBackoffConfig: defaultExpBackoffConfig(), ctx: ctx}
}

// expBackoffWithContext wraps expBackoff with a context.
func expBackoffWithContext(f func() (bool, error), cfg ExpBackoffConfigWithContext) error {
	err := expBackoff(f, cfg.ExpBackoffConfig, func() bool {
		return cfg.ctx.Err() != nil
	})
	switch {
	case errors.Is(err, backoffStoppedError{}):
		return fmt.Errorf("exp backoff context cancelled %w", cfg.ctx.Err())
	case err != nil:
		return fmt.Errorf("exp backoff cancelled: %w", err)
	default:
		return nil
	}
}