package main

import (
	"testing"
	"time"
)

func TestSignCallbackPayload_KnownVector(t *testing.T) {
	secret := "s3cr3t"
	timestamp := "1700000000"
	payload := `{"a":1}`

	got := signCallbackPayload(secret, timestamp, payload)
	want := "8dbbbbf4523b10bbb793e74d854144c45acccc2d233667b1c06b805b6ded8a84"
	if got != want {
		t.Fatalf("signature mismatch: got=%s want=%s", got, want)
	}
}

func TestCalculateRetryDelay(t *testing.T) {
	base := 10 * time.Second
	cases := []struct {
		retryCount int
		want       time.Duration
	}{
		{retryCount: 1, want: 10 * time.Second},
		{retryCount: 2, want: 20 * time.Second},
		{retryCount: 3, want: 40 * time.Second},
		{retryCount: 4, want: 80 * time.Second},
		// capped at 2^6
		{retryCount: 8, want: 640 * time.Second},
		{retryCount: 50, want: 640 * time.Second},
	}

	for _, c := range cases {
		if got := calculateRetryDelay(base, c.retryCount); got != c.want {
			t.Fatalf("retryCount=%d got=%s want=%s", c.retryCount, got, c.want)
		}
	}
}

func TestCalculateConfirmedCutoff(t *testing.T) {
	if got := calculateConfirmedCutoff(100, 1); got != 100 {
		t.Fatalf("minConfirmations=1 got=%d want=%d", got, 100)
	}
	if got := calculateConfirmedCutoff(100, 3); got != 98 {
		t.Fatalf("minConfirmations=3 got=%d want=%d", got, 98)
	}
	if got := calculateConfirmedCutoff(1, 3); got != 0 {
		t.Fatalf("underflow got=%d want=%d", got, 0)
	}
}
