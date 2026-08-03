package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckerReportsRequiredAndOptionalFailures(t *testing.T) {
	checker := NewChecker(time.Second,
		Check{Name: "mysql", Required: true, Run: func(context.Context) error { return nil }},
		Check{Name: "rabbitmq", Run: func(context.Context) error { return errors.New("private connection detail") }},
	)
	checker.MarkReady()
	report, ready := checker.Probe(context.Background())
	if !ready || report.Status != "degraded" {
		t.Fatalf("optional failure report = %#v, ready=%v", report, ready)
	}
	if report.Checks["rabbitmq"].ErrorCode != "unavailable" {
		t.Fatalf("raw errors should not be exposed: %#v", report.Checks["rabbitmq"])
	}

	checker = NewChecker(time.Second,
		Check{Name: "mysql", Required: true, Run: func(context.Context) error { return errors.New("down") }},
	)
	checker.MarkReady()
	report, ready = checker.Probe(context.Background())
	if ready || report.Status != "unavailable" {
		t.Fatalf("required failure report = %#v, ready=%v", report, ready)
	}
}

func TestCheckerStopsProbingWhenDraining(t *testing.T) {
	called := false
	checker := NewChecker(time.Second, Check{Name: "mysql", Required: true, Run: func(context.Context) error {
		called = true
		return nil
	}})
	report, ready := checker.Probe(context.Background())
	if ready || report.Status != "unavailable" || called {
		t.Fatalf("not-ready checker probed dependencies: report=%#v ready=%v called=%v", report, ready, called)
	}
}

func TestCheckerEnforcesOverallTimeout(t *testing.T) {
	checker := NewChecker(20*time.Millisecond, Check{Name: "mysql", Required: true, Run: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	checker.MarkReady()
	started := time.Now()
	report, ready := checker.Probe(context.Background())
	if ready || report.Checks["mysql"].ErrorCode != "timeout" {
		t.Fatalf("timeout report = %#v, ready=%v", report, ready)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("probe exceeded its budget: %s", elapsed)
	}
}
