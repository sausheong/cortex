package cortex

import "strings"

// splitContent breaks long content into chunks of roughly maxChars characters,
// preferring paragraph (\n\n) boundaries, then sentence boundaries, then a
// hard cut. If maxChars <= 0 or the content fits, the original content is
// returned as a single-element slice.
func splitContent(content string, maxChars int) []string {
	if maxChars <= 0 || len(content) <= maxChars {
		return []string{content}
	}

	// First pass: split on blank-line paragraphs and greedily group them.
	parts := splitGreedy(strings.Split(content, "\n\n"), "\n\n", maxChars)

	// Second pass: any single part still too long gets split further on
	// sentence boundaries, then hard-cut as a last resort.
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) <= maxChars {
			out = append(out, p)
			continue
		}
		sentences := splitSentences(p)
		out = append(out, splitGreedy(sentences, " ", maxChars)...)
	}

	// Third pass: hard cut anything still oversized (e.g. a 20KB single line).
	final := make([]string, 0, len(out))
	for _, p := range out {
		for len(p) > maxChars {
			final = append(final, p[:maxChars])
			p = p[maxChars:]
		}
		if p != "" {
			final = append(final, p)
		}
	}
	return final
}

// splitGreedy concatenates pieces back together with sep, starting a new
// chunk whenever adding the next piece would exceed maxChars.
func splitGreedy(pieces []string, sep string, maxChars int) []string {
	var (
		out []string
		cur strings.Builder
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, p := range pieces {
		if cur.Len() == 0 {
			cur.WriteString(p)
			continue
		}
		if cur.Len()+len(sep)+len(p) > maxChars {
			flush()
			cur.WriteString(p)
			continue
		}
		cur.WriteString(sep)
		cur.WriteString(p)
	}
	flush()
	return out
}

// splitSentences splits text on sentence-ending punctuation followed by
// whitespace. It is intentionally simple — we don't need linguistic
// correctness, just reasonable boundaries to keep embeddings coherent.
func splitSentences(text string) []string {
	var (
		out   []string
		start int
	)
	for i := 0; i < len(text)-1; i++ {
		c := text[i]
		if (c == '.' || c == '!' || c == '?') && (text[i+1] == ' ' || text[i+1] == '\n') {
			out = append(out, strings.TrimSpace(text[start:i+1]))
			start = i + 2
		}
	}
	if start < len(text) {
		if tail := strings.TrimSpace(text[start:]); tail != "" {
			out = append(out, tail)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}
