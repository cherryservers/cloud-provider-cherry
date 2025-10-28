package test

import (
	"errors"
	"math/rand"
	"time"
)

// start, max and timeout fields are defined in seconds
type ExpBackoffConfig struct {
	start   int
	exp     int
	max     int
	timeout int
}

func defaultExpBackoffConfig() ExpBackoffConfig {
	return ExpBackoffConfig{start: 2, exp: 2, max: 32, timeout: 513}
}

// backoff until f returns true or non-nil error
func expBackoff(f func() (bool, error), cfg ExpBackoffConfig) error {
	dur := cfg.start
	for elapsed := 0; elapsed < cfg.timeout; {
		r, err := f()
		if err != nil || r {
			return err
		}
		time.Sleep(time.Duration(dur)*time.Second + time.Duration(rand.Intn(900)+100)*time.Millisecond)
		elapsed += dur
		dur *= cfg.exp
		if dur > cfg.max {
			dur = cfg.max
		}
	}
	return errors.New("exp backoff timeout")
}
