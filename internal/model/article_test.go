package model

import (
	"encoding/json"
	"errors"
	"testing"
)

func walkObjects(t *testing.T, path string, n any, fn func(path string, obj map[string]any)) {
	t.Helper()
	switch v := n.(type) {
	case map[string]any:
		if v["type"] == "object" {
			fn(path, v)
		}
		for k, c := range v {
			walkObjects(t, path+"/"+k, c, fn)
		}
	case []any:
		for _, c := range v {
			walkObjects(t, path, c, fn)
		}
	}
}

func TestArticleSchemaStrictAndRequiresSources(t *testing.T) {
	b, err := json.Marshal(ArticleJSONSchema())
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	objects := 0
	walkObjects(t, "", s, func(path string, o map[string]any) {
		objects++
		if o["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties not false", path)
		}
		props, _ := o["properties"].(map[string]any)
		req, _ := o["required"].([]any)
		if len(req) != len(props) {
			t.Errorf("%s: required %d != properties %d (strict mode needs all)", path, len(req), len(props))
		}
	})
	if objects != 3 {
		t.Fatalf("objects = %d, want 3", objects)
	}
	props := s["properties"].(map[string]any)
	ls := props["lead_sources"].(map[string]any)
	if ls["minItems"] != float64(1) {
		t.Errorf("lead_sources minItems = %v", ls["minItems"])
	}
	item := props["sections"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	src := item["properties"].(map[string]any)["sources"].(map[string]any)
	if src["minItems"] != float64(1) {
		t.Errorf("item sources minItems = %v", src["minItems"])
	}
}

func TestParseArticle(t *testing.T) {
	a, err := ParseArticle("```json\n{\"title\":\"\",\"lead\":\"L\",\"lead_sources\":[\"m1\"],\"sections\":[],\"related\":[]}\n```")
	if err != nil || a.Lead != "L" || a.LeadSources[0] != "m1" {
		t.Fatalf("got %+v, %v", a, err)
	}
	if _, err := ParseArticle("not json"); !errors.Is(err, ErrArticleBadOutput) {
		t.Fatalf("err = %v", err)
	}
}

func TestArticleRequestJSONLengthDefault(t *testing.T) {
	s, err := ArticleRequestJSON(ArticleInput{}, "bogus")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	if m["length"] != "standard" {
		t.Fatalf("length = %v", m["length"])
	}
}
