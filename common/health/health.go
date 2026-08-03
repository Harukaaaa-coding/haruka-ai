package health

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type Check struct {
	Name     string
	Required bool
	Run      func(context.Context) error
}

type Component struct {
	Status    string `json:"status"`
	Required  bool   `json:"required"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type Report struct {
	Status string               `json:"status"`
	Checks map[string]Component `json:"checks"`
}

type Checker struct {
	timeout   time.Duration
	checks    []Check
	accepting atomic.Bool
}

func NewChecker(timeout time.Duration, checks ...Check) *Checker {
	if timeout <= 0 {
		timeout = time.Second
	}
	return &Checker{timeout: timeout, checks: append([]Check(nil), checks...)}
}

func (checker *Checker) MarkReady() {
	if checker != nil {
		checker.accepting.Store(true)
	}
}

func (checker *Checker) MarkNotReady() {
	if checker != nil {
		checker.accepting.Store(false)
	}
}

func (checker *Checker) Accepting() bool {
	return checker != nil && checker.accepting.Load()
}

type checkResult struct {
	name      string
	component Component
}

func (checker *Checker) Probe(ctx context.Context) (Report, bool) {
	report := Report{Status: "unavailable", Checks: make(map[string]Component)}
	if checker == nil || !checker.Accepting() {
		report.Checks["lifecycle"] = Component{Status: "down", Required: true, ErrorCode: "stopped"}
		return report, false
	}
	if len(checker.checks) == 0 {
		report.Status = "ok"
		return report, true
	}

	probeCtx, cancel := context.WithTimeout(ctx, checker.timeout)
	defer cancel()
	results := make(chan checkResult, len(checker.checks))
	for _, configured := range checker.checks {
		check := configured
		go func() {
			started := time.Now()
			err := runSafely(probeCtx, check.Run)
			component := Component{Status: "up", Required: check.Required, LatencyMS: time.Since(started).Milliseconds()}
			if err != nil {
				component.Status = "down"
				component.ErrorCode = classifyError(err)
			}
			results <- checkResult{name: check.Name, component: component}
		}()
	}

	pending := make(map[string]Check, len(checker.checks))
	for _, check := range checker.checks {
		pending[check.Name] = check
	}
	for len(pending) > 0 {
		select {
		case result := <-results:
			report.Checks[result.name] = result.component
			delete(pending, result.name)
		case <-probeCtx.Done():
			errorCode := classifyError(probeCtx.Err())
			for name, check := range pending {
				report.Checks[name] = Component{Status: "down", Required: check.Required, ErrorCode: errorCode}
			}
			pending = nil
		}
	}

	report.Status = "ok"
	ready := true
	for _, component := range report.Checks {
		if component.Status == "up" {
			continue
		}
		if component.Required {
			report.Status = "unavailable"
			ready = false
			break
		}
		report.Status = "degraded"
	}
	return report, ready
}

func runSafely(ctx context.Context, run func(context.Context) error) (err error) {
	if run == nil {
		return errors.New("health check is unavailable")
	}
	defer func() {
		if recover() != nil {
			err = errors.New("health check panicked")
		}
	}()
	return run(ctx)
}

func classifyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "stopped"
	}
	return "unavailable"
}
