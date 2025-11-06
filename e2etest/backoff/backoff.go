package backoff

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

func DefaultExpBackoffConfig() ExpBackoffConfig {
	return ExpBackoffConfig{
		start: 2 * time.Second,
		exp:   2,
		max:   32 * time.Second,
	}
}

// ExpBackoff retries f until it returns true, a non-nil error or
// stop returns true.
func ExpBackoff(f func() (bool, error), cfg ExpBackoffConfig, stop func() bool) error {
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

func DefaultExpBackoffConfigWithContext(ctx context.Context) ExpBackoffConfigWithContext {
	return ExpBackoffConfigWithContext{ExpBackoffConfig: DefaultExpBackoffConfig(), ctx: ctx}
}

// ExpBackoffWithContext wraps expBackoff with a context.
func ExpBackoffWithContext(f func() (bool, error), cfg ExpBackoffConfigWithContext) error {
	err := ExpBackoff(f, cfg.ExpBackoffConfig, func() bool {
		return cfg.ctx.Err() != nil
	})
	switch {
	case errors.Is(err, backoffStoppedError{}):
		return fmt.Errorf("exp backoff context cancelled: %w", cfg.ctx.Err())
	case err != nil:
		return fmt.Errorf("error while doing exp backoff: %w", err)
	default:
		return nil
	}
}
