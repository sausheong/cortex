package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ArticleWriter is an optional capability a provider may implement to write
// a sourced, linked article about one subject. It is type-asserted like
// Reconciler. system holds only fixed instructions; every piece of user data
// travels in the ArticleInput, which a provider must send as one JSON
// document in the user message and never concatenate into the system prompt.
//
// length is one of "short", "standard" or "long"; anything else is treated as
// "standard". The returned Article is unvalidated: callers check source ids
// and links themselves.
type ArticleWriter interface {
	WriteArticle(ctx context.Context, system string, in ArticleInput, length string) (Article, ArticleUsage, error)
}

// ArticleUsage is the token usage a provider reported for one article call.
// Both fields are zero when the provider did not report usage.
type ArticleUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ArticleInput is everything the writer is given about a subject.
type ArticleInput struct {
	Subject    ArticleSubject    `json:"subject"`
	Owner      string            `json:"owner"`
	Memories   []ArticleMemory   `json:"memories"`
	Relations  []ArticleRelation `json:"relations"`
	Linkable   []string          `json:"linkable"`
	Attributes map[string]any    `json:"attributes,omitempty"`
	Aliases    []string          `json:"aliases,omitempty"`
}

// ArticleSubject names what the article is about. Kind is "entity" or
// "topic"; Members lists the member entity names of a topic cluster.
type ArticleSubject struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Members []string `json:"members,omitempty"`
}

// ArticleMemory is one live memory offered as a source. ID is the only valid
// value for a citation.
type ArticleMemory struct {
	ID         string  `json:"id"`
	Content    string  `json:"content"`
	When       string  `json:"when,omitempty"`
	Speaker    string  `json:"speaker,omitempty"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence"`
	Correction bool    `json:"correction,omitempty"`
}

// ArticleRelation is a graph edge around the subject.
type ArticleRelation struct {
	Type            string `json:"type"`
	Other           string `json:"other"`
	OtherDescriptor string `json:"other_descriptor,omitempty"`
	Direction       string `json:"direction"`
}

// Article is the writer's structured output. Title is used for topics only.
type Article struct {
	Title       string           `json:"title"`
	Lead        string           `json:"lead"`
	LeadSources []string         `json:"lead_sources"`
	Sections    []ArticleSection `json:"sections"`
	Related     []string         `json:"related"`
}

// ArticleSection is a headed group of items.
type ArticleSection struct {
	Heading string        `json:"heading"`
	Items   []ArticleItem `json:"items"`
}

// ArticleItem is one paragraph or bullet with the memory ids that support it
// and the names of linkable subjects it mentions.
type ArticleItem struct {
	Text    string   `json:"text"`
	Bullet  bool     `json:"bullet"`
	Sources []string `json:"sources"`
	Links   []string `json:"links"`
}

var (
	// ErrArticleTruncated is returned when the model hit its output limit
	// before finishing the article.
	ErrArticleTruncated = errors.New("cortex: article output truncated")
	// ErrArticleBadOutput is returned when the model output is not a
	// parseable article.
	ErrArticleBadOutput = errors.New("cortex: article output is not valid")
)

// ArticleJSONSchema returns the strict JSON Schema of Article. Every object
// sets additionalProperties false and lists all its properties as required;
// the lead and every item must carry at least one source.
func ArticleJSONSchema() map[string]any {
	str := map[string]any{"type": "string"}
	strs := func(min int) map[string]any {
		s := map[string]any{"type": "array", "items": str}
		if min > 0 {
			s["minItems"] = min
		}
		return s
	}
	item := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"text", "bullet", "sources", "links"},
		"properties": map[string]any{
			"text":    str,
			"bullet":  map[string]any{"type": "boolean"},
			"sources": strs(1),
			"links":   strs(0),
		},
	}
	section := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"heading", "items"},
		"properties": map[string]any{
			"heading": str,
			"items":   map[string]any{"type": "array", "items": item},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"title", "lead", "lead_sources", "sections", "related"},
		"properties": map[string]any{
			"title":        str,
			"lead":         str,
			"lead_sources": strs(1),
			"sections":     map[string]any{"type": "array", "items": section},
			"related":      strs(0),
		},
	}
}

// ParseArticle decodes the model's JSON output into an Article, tolerating a
// surrounding code fence. Any failure wraps ErrArticleBadOutput.
func ParseArticle(raw string) (Article, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	var a Article
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return Article{}, fmt.Errorf("%w: %v", ErrArticleBadOutput, err)
	}
	return a, nil
}

// ArticleRequestJSON builds the single user message sent to a provider: the
// requested length plus the input, as one JSON document.
func ArticleRequestJSON(in ArticleInput, length string) (string, error) {
	switch length {
	case "short", "standard", "long":
	default:
		length = "standard"
	}
	b, err := json.Marshal(struct {
		Length string       `json:"length"`
		Input  ArticleInput `json:"input"`
	}{length, in})
	if err != nil {
		return "", err
	}
	return string(b), nil
}
