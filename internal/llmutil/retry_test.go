package llmutil

import (
	"context"
	"errors"
	"testing"
)

func TestIsTransientLLMErr(t *testing.T) {
	for _, e := range []error{
		errors.New(`openai: detect conflicts: Post "x": net/http: timeout awaiting response headers`),
		errors.New("error, status code: 429, status: 429 Too Many Requests"),
		errors.New("error, status code: 503, message: unavailable"),
		context.DeadlineExceeded,
	} {
		if !IsTransientLLMErr(e) {
			t.Errorf("want transient: %v", e)
		}
	}
	for _, e := range []error{errors.New("error, status code: 401, message: bad key"), errors.New("invalid json"), nil} {
		if IsTransientLLMErr(e) {
			t.Errorf("want permanent: %v", e)
		}
	}
}
