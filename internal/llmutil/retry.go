package llmutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// llmMaxRetries is how many times a transient LLM failure is retried (so a
// call is attempted at most llmMaxRetries+1 times).
const llmMaxRetries = 2

// maxReportErrors bounds how many error strings a pass report carries.
const maxReportErrors = 5

// Backoff returns the wait before retry number attempt (1-based). It is a
// variable so tests can make it instant.
var Backoff = func(attempt int) time.Duration {
	return time.Duration(attempt) * 3 * time.Second
}

var transientStatusRE = regexp.MustCompile(`\b(429|5\d\d)\b`)

// IsTransientLLMErr reports whether err looks like a retryable provider
// failure: a timeout, a rate limit (429) or a server error (5xx).
func IsTransientLLMErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"timeout", "timed out", "deadline exceeded", "rate limit", "overloaded",
		"connection reset", "unexpected eof", "temporarily unavailable", "too many requests"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return transientStatusRE.MatchString(s)
}

// RetryLLM runs fn, retrying transient failures up to llmMaxRetries times with
// backoff. Non-transient errors, and any error once ctx is done, return at once.
func RetryLLM[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var out T
	var err error
	for attempt := 0; ; attempt++ {
		out, err = fn()
		if err == nil || ctx.Err() != nil || attempt >= llmMaxRetries || !IsTransientLLMErr(err) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(Backoff(attempt + 1)):
		}
	}
}

// AddReportError appends a bounded, truncated error string.
func AddReportError(errs *[]string, format string, args ...any) {
	if len(*errs) >= maxReportErrors {
		return
	}
	s := fmt.Sprintf(format, args...)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	*errs = append(*errs, s)
}
