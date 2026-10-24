package memory

import (
	"strings"
	"testing"
)

func TestCheckPgvectorVersion(t *testing.T) {
	for _, v := range []string{"0.8.0", "0.8.1", "1.0.0"} {
		if err := checkPgvectorVersion(v); err != nil {
			t.Errorf("%s: unexpected error %v", v, err)
		}
	}
	err := checkPgvectorVersion("0.7.4")
	if err == nil || !strings.Contains(err.Error(), "0.8.0") {
		t.Errorf("0.7.4: want error mentioning 0.8.0, got %v", err)
	}
}
