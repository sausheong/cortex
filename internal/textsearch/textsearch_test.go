package textsearch

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/pgtest"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func pgLexemes(t *testing.T, db *sql.DB, text string) []string {
	t.Helper()
	var arr string
	err := db.QueryRow(`SELECT array_to_string(tsvector_to_array(to_tsvector('english', left($1, 200000))), E'\x01')`, text).Scan(&arr)
	if err != nil {
		t.Fatal(err)
	}
	if arr == "" {
		return nil
	}
	out := strings.Split(arr, "\x01")
	sort.Strings(out)
	return out
}

func goSet(text string) []string {
	out := distinct(Lexemes(text))
	sort.Strings(out)
	return out
}

var parityTexts = []string{
	"running", "memories", "the quick brown fox", "Contact a.b@example.com now",
	"see https://www.example.com/path/file.html?x=1 ok", "visit www.example.com today",
	"state-of-the-art design", "running-late quickly-moving",
	"version 3.14 and 2024 and 1,000", "café Zoë naïve résumé",
	"foo_bar x-ray abc123 10km", "UPPER.Case@Example.COM",
	"it's O'Brien's dog", "5% growth, 2024-01-05 launch", "e.g. node.js file.txt",
	"Alice moved to Berlin in March; she likes hiking.",
	"The databases were indexed quickly, generously and happily",
}

func TestLexemesMatchPostgresStemming(t *testing.T) {
	db := openDB(t)
	for _, text := range parityTexts {
		want := pgLexemes(t, db, text)
		got := goSet(text)
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("text %q\n pg: %q\n go: %q", text, want, got)
		}
	}
}

func TestLexemesBasics(t *testing.T) {
	got := Lexemes("The running memories")
	if fmt.Sprint(got) != "[run memori]" {
		t.Fatalf("got %q", got)
	}
}

func corpus() []Doc {
	topics := []string{"hiking", "python", "coffee", "berlin", "marathon", "piano", "garden", "budget", "sailing", "chess"}
	var docs []Doc
	for i := 0; i < 40; i++ {
		a, b := topics[i%10], topics[(i*3+1)%10]
		docs = append(docs, Doc{
			ID: fmt.Sprintf("m%02d", i),
			Text: fmt.Sprintf("Memory %d: the user enjoys %s and sometimes %s with friends. Contact user%d@example.com about %s-related plans, version 1.%d.",
				i, a, b, i, a, i),
		})
	}
	docs[7].Text += " Also loves state-of-the-art cafés."
	docs[19].Text = "Zoë runs marathons quickly"
	return docs
}

const memRankSQL = `WITH d AS (
  SELECT tok.q AS q, (SELECT count(*) FROM memories x WHERE x.tsv @@ tok.q) AS n
  FROM (VALUES %s) AS tok(q)
), tot AS (SELECT GREATEST(count(*), 1)::float8 AS c FROM memories)
SELECT m.id FROM memories m JOIN d ON m.tsv @@ d.q CROSS JOIN tot
GROUP BY m.id
ORDER BY sum(ln(1 + tot.c / GREATEST(d.n, 1))) / sqrt(GREATEST(array_length(tsvector_to_array(m.tsv), 1), 1)) DESC, m.id
LIMIT 10`

func TestMemScoreMatchesSQLRanking(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE memories (id TEXT PRIMARY KEY, content TEXT NOT NULL,
		tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', left(content, 200000))) STORED)`); err != nil {
		t.Fatal(err)
	}
	docs := corpus()
	for _, d := range docs {
		if _, err := db.Exec(`INSERT INTO memories (id, content) VALUES ($1, $2)`, d.ID, d.Text); err != nil {
			t.Fatal(err)
		}
	}
	ix := BuildMemIndex(docs)
	queries := []string{
		"hiking", "python coffee", "what does the user enjoy about hiking", "user7@example.com",
		"state-of-the-art", "cafés", "marathons quickly", "version 1.12", "sailing chess piano",
		"hiking hiking", "berlin-related plans", "Zoë",
	}
	for _, q := range queries {
		var vals []string
		var args []any
		for i, f := range strings.Fields(q) {
			vals = append(vals, fmt.Sprintf("(plainto_tsquery('english', $%d))", i+1))
			args = append(args, f)
		}
		rows, err := db.Query(fmt.Sprintf(memRankSQL, strings.Join(vals, ", ")), args...)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			want = append(want, id)
		}
		rows.Close()
		var got []string
		for _, h := range ix.Search(q, 10, nil) {
			got = append(got, h.ID)
		}
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("query %q\n sql: %v\n go:  %v", q, want, got)
		}
	}
}

func TestMemScoreIDFPrefersRareTerms(t *testing.T) {
	docs := []Doc{{"a", "common common word"}, {"b", "common word rare"}, {"c", "common thing"}, {"d", "common stuff"}}
	hits := BuildMemIndex(docs).Search("common rare", 5, nil)
	if len(hits) == 0 || hits[0].ID != "b" {
		t.Fatalf("hits=%v", hits)
	}
}

func TestMemScoreEmptyQueryNoResults(t *testing.T) {
	ix := BuildMemIndex([]Doc{{"a", "hello world"}})
	for _, q := range []string{"", "   ", "the of"} {
		if h := ix.Search(q, 5, nil); len(h) != 0 {
			t.Fatalf("%q: %v", q, h)
		}
	}
}

func TestMemScoreKeepAndTieBreak(t *testing.T) {
	ix := BuildMemIndex([]Doc{{"b", "apple"}, {"a", "apple"}, {"c", "apple"}})
	h := ix.Search("apple", 0, func(id string) bool { return id != "c" })
	if len(h) != 2 || h[0].ID != "a" || h[1].ID != "b" {
		t.Fatalf("%v", h)
	}
}

func TestBM25RanksTermFrequencyAndLength(t *testing.T) {
	docs := []Doc{
		{"long", "cat " + strings.Repeat("filler ", 60)},
		{"short", "cat"},
		{"tf", "cat cat cat dog"},
		{"none", "dog"},
	}
	h := BuildBM25(docs).Search("cat", 10, nil)
	if len(h) != 3 || h[0].ID != "tf" && h[0].ID != "short" || h[2].ID != "long" {
		t.Fatalf("%v", h)
	}
	if h[0].ID != "short" && h[0].ID != "tf" {
		t.Fatal("unexpected top")
	}
}

func TestBM25KeepFilterAndLimit(t *testing.T) {
	docs := []Doc{{"a", "apple pie"}, {"b", "apple tart"}, {"c", "apple crumble"}}
	ix := BuildBM25(docs)
	h := ix.Search("apple", 1, func(id string) bool { return id != "a" })
	if len(h) != 1 || h[0].ID == "a" {
		t.Fatalf("%v", h)
	}
	if h := ix.Search("", 5, nil); len(h) != 0 {
		t.Fatal("empty query returned hits")
	}
}

func TestConcurrentSearch(t *testing.T) {
	m := BuildMemIndex(corpus())
	b := BuildBM25(corpus())
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			m.Search("hiking python", 5, nil)
			b.Search("hiking python", 5, nil)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
