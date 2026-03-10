package main

import (
	"context"
	"time"
)

type RateLimiter struct {
	ticker *time.Ticker
}

func NewRateLimiter(qps float64) *RateLimiter {
	if qps <= 0 {
		return nil
	}
	interval := time.Duration(float64(time.Second) / qps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	return &RateLimiter{ticker: time.NewTicker(interval)}
}

func (l *RateLimiter) Wait(ctx context.Context) error {
	if l == nil || l.ticker == nil {
		return nil
	}
	select {
	case <-l.ticker.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
