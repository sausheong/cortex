package cortex

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// checkPgvectorVersion returns an error if v is older than 0.8.0.
func checkPgvectorVersion(v string) error {
	parts := strings.Split(strings.TrimSpace(v), ".")
	nums := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return fmt.Errorf("cortex: can't parse pgvector version %q", v)
		}
		nums[i] = n
	}
	if nums[0] > 0 || nums[1] >= 8 {
		return nil
	}
	return fmt.Errorf("cortex: pgvector %s is too old, need >= 0.8.0 (ALTER EXTENSION vector UPDATE)", v)
}

// ensureVectorSchema checks the pgvector version. The embedding column and
// its indexes are managed per user by migration 002 and the vector layer.
func (c *Cortex) ensureVectorSchema(ctx context.Context) error {
	var ver string
	if err := c.db.QueryRowContext(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&ver); err != nil {
		return fmt.Errorf("cortex: pgvector extension not found: %w", err)
	}
	return checkPgvectorVersion(ver)
}

// vectorLiteral renders v in pgvector text form, e.g. [0.1,0.2].
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// parseVectorLiteral parses pgvector text form into float32s.
func parseVectorLiteral(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}
