package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Alias kinds. A name alias is another name for the entity; the others are
// identifiers that name exactly one entity regardless of its type.
const (
	AliasName   = "name"
	AliasEmail  = "email"
	AliasPhone  = "phone"
	AliasHandle = "handle"
)

// ErrAliasTaken is returned by AddAlias when the alias already belongs to a
// different entity.
var ErrAliasTaken = errors.New("cortex: alias already belongs to another entity")

// Alias is one stable identifier of an entity.
type Alias struct {
	EntityID  string    `json:"entity_id"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// DetectAliasKind guesses the kind of an identifier: an email address, a
// phone number (seven or more digits, optionally formatted), an @handle, or
// otherwise a name.
func DetectAliasKind(value string) string {
	v := strings.TrimSpace(value)
	switch {
	case emailRe.MatchString(v):
		return AliasEmail
	case strings.HasPrefix(v, "@") && len(v) > 1 && !strings.ContainsAny(v, " \t"):
		return AliasHandle
	case isPhoneLike(v):
		return AliasPhone
	default:
		return AliasName
	}
}

func isPhoneLike(v string) bool {
	digits := 0
	for _, r := range v {
		switch {
		case unicode.IsDigit(r):
			digits++
		case strings.ContainsRune("+-() .", r):
		default:
			return false
		}
	}
	return digits >= 7
}

// normalizeAlias returns the comparison key for an alias value: emails and
// names lower-cased with whitespace collapsed, phone numbers reduced to
// their digits (keeping a leading +), handles without the leading @.
func normalizeAlias(kind, value string) string {
	v := strings.TrimSpace(value)
	switch kind {
	case AliasPhone:
		var b strings.Builder
		for i, r := range v {
			if unicode.IsDigit(r) || (r == '+' && i == 0) {
				b.WriteRune(r)
			}
		}
		return b.String()
	case AliasHandle:
		return strings.ToLower(strings.TrimPrefix(v, "@"))
	default:
		return strings.ToLower(spaceRe.ReplaceAllString(v, " "))
	}
}

// AddAlias records value as an identifier of entityID. kind is one of the
// Alias* constants; empty means detect it with DetectAliasKind. Adding an
// alias the entity already has is a no-op; one that belongs to another live
// entity fails with ErrAliasTaken.
func (c *Cortex) AddAlias(ctx context.Context, entityID, kind, value string) error {
	if err := requireEntity(ctx, c.db, c.userID, entityID); err != nil {
		return err
	}
	if kind == "" {
		kind = DetectAliasKind(value)
	}
	norm := normalizeAlias(kind, value)
	if norm == "" {
		return fmt.Errorf("cortex: empty alias")
	}
	var owner string
	err := c.db.QueryRowContext(ctx,
		`INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm, source)
		 VALUES (?, ?, ?, ?, ?, 'user')
		 ON CONFLICT (user_id, kind, value_norm) DO UPDATE SET
		   entity_id = CASE WHEN EXISTS (SELECT 1 FROM entities e WHERE e.id = entity_aliases.entity_id AND e.deleted_at IS NULL)
		                    THEN entity_aliases.entity_id ELSE EXCLUDED.entity_id END
		 RETURNING entity_id`,
		c.userID, entityID, kind, strings.TrimSpace(value), norm,
	).Scan(&owner)
	if err != nil {
		return fmt.Errorf("cortex: add alias: %w", err)
	}
	if owner != entityID {
		return ErrAliasTaken
	}
	return nil
}

// addAliasQuiet records an alias learned during ingest. It never moves an
// alias that already exists and never fails the ingest over one.
func (c *Cortex) addAliasQuiet(ctx context.Context, entityID, kind, value, source string) {
	norm := normalizeAlias(kind, value)
	if norm == "" {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm, source)
		 VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		c.userID, entityID, kind, strings.TrimSpace(value), norm, source); err != nil {
		c.logf("cortex: record alias %q: %v", value, err)
	}
}

// Aliases returns the identifiers recorded for an entity.
func (c *Cortex) Aliases(ctx context.Context, entityID string) ([]Alias, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT entity_id, kind, value, COALESCE(source, ''), created_at FROM entity_aliases
		 WHERE user_id = ? AND entity_id = ? ORDER BY kind, value_norm`, c.userID, entityID)
	if err != nil {
		return nil, fmt.Errorf("cortex: list aliases: %w", err)
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.EntityID, &a.Kind, &a.Value, &a.Source, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan alias: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RemoveAlias deletes an alias. kind empty means detect it from value.
func (c *Cortex) RemoveAlias(ctx context.Context, kind, value string) error {
	if kind == "" {
		kind = DetectAliasKind(value)
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM entity_aliases WHERE user_id = ? AND kind = ? AND value_norm = ?`,
		c.userID, kind, normalizeAlias(kind, value)); err != nil {
		return fmt.Errorf("cortex: remove alias: %w", err)
	}
	return nil
}

// Resolution methods, in the order ingest tries them.
const (
	ResolvedByAlias = "alias"
	ResolvedByOwner = "owner"
	ResolvedByExact = "exact"
	ResolvedByFuzzy = "fuzzy"
	ResolvedByLLM   = "llm"
)

// Resolution is the outcome of resolving a mention to a stored entity.
// Entity is nil when no candidate was confident enough; Candidates then
// holds the closest stored entities, best first.
type Resolution struct {
	Entity     *Entity        `json:"entity,omitempty"`
	Method     string         `json:"method,omitempty"`
	Score      float64        `json:"score"`
	Candidates []ScoredEntity `json:"candidates,omitempty"`
}

// ScoredEntity is a resolution candidate with its match score in [0, 1].
type ScoredEntity struct {
	Entity Entity  `json:"entity"`
	Score  float64 `json:"score"`
}

// EntityMention is what an EntityDisambiguator sees: the extracted name and
// type, and the text it came from.
type EntityMention struct {
	Name    string
	Type    string
	Context string
}

// EntityChoice is a disambiguator's verdict: the chosen candidate's ID, or
// empty for "none of these — it is a new entity", with a confidence in [0, 1].
type EntityChoice struct {
	ID         string  `json:"id"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// EntityDisambiguator is an optional LLM capability: given a mention and a
// short list of plausible stored entities, pick the one it refers to.
// Resolution asks it only when name similarity alone is inconclusive.
type EntityDisambiguator interface {
	DisambiguateEntity(ctx context.Context, mention EntityMention, candidates []Entity) (EntityChoice, error)
}

// Resolution thresholds. A fuzzy match links automatically at or above
// fuzzyAutoLink when it also leads the runner-up by fuzzyMargin; between
// fuzzyAsk and fuzzyAutoLink an EntityDisambiguator (if configured) decides,
// and its choice is taken at or above llmMinConfidence.
const (
	fuzzyAutoLink    = 0.85
	fuzzyMargin      = 0.10
	fuzzyAsk         = 0.50
	llmMinConfidence = 0.70
	proximityBoost   = 0.10
	fuzzyCandidates  = 500
	disambiguateTopK = 5
)

// userMentions are how an assistant writing to the graph refers to its
// owner; firstPerson words refer to the owner only when the owner is the
// speaker (in an ingested email, "I" is the sender).
var (
	userMentions = map[string]bool{"user": true, "the user": true}
	firstPerson  = map[string]bool{"i": true, "me": true, "myself": true}
)

// isSelfMention reports whether key (a normalised name) refers to the owner
// given who is speaking.
func isSelfMention(key string, rc *resolveContext) bool {
	return userMentions[key] || (rc.ownerSpeaks && firstPerson[key])
}

// ResolveEntity resolves a mention to the stored entity it most likely names,
// without creating anything or consulting an LLM. typ narrows name matching
// to one (normalised) type; empty matches any type. Identifier aliases
// (email, phone, handle) match regardless of type.
func (c *Cortex) ResolveEntity(ctx context.Context, mention, typ string) (*Resolution, error) {
	rc := &resolveContext{}
	if typ != "" {
		typ, _ = CanonicalEntityType(typ)
	}
	return c.resolve(ctx, mention, typ, nil, rc, false)
}

// resolveContext carries what one Remember call knows while it resolves its
// entities: the source text, entities resolved so far, and the owner.
type resolveContext struct {
	content     string
	ownerSpeaks bool // the text is in the owner's voice
	resolved    []string
	ownerID     string
	ownerNames  map[string]bool
	ownerReady  bool
}

// owner loads the owner entity and the names it goes by, once per context.
func (c *Cortex) owner(ctx context.Context, rc *resolveContext) (string, map[string]bool) {
	if rc.ownerReady {
		return rc.ownerID, rc.ownerNames
	}
	rc.ownerReady = true
	rc.ownerNames = map[string]bool{}
	id, err := c.OwnerEntityID(ctx)
	if err != nil {
		return "", rc.ownerNames
	}
	var entName, userName, email sql.NullString
	if err := c.db.QueryRowContext(ctx,
		`SELECT e.name, u.name, u.email FROM users u JOIN entities e ON e.id = u.owner_entity_id
		 WHERE u.id = ? AND e.deleted_at IS NULL`, c.userID).Scan(&entName, &userName, &email); err != nil {
		return "", rc.ownerNames
	}
	for _, n := range []string{entName.String, userName.String} {
		if k := normalizeAlias(AliasName, n); k != "" {
			rc.ownerNames[k] = true
		}
	}
	if k := normalizeAlias(AliasEmail, email.String); k != "" {
		rc.ownerNames[k] = true
	}
	rc.ownerID = id
	return id, rc.ownerNames
}

// resolve runs the resolution cascade: identifier and name aliases, the
// owner, an exact name, then fuzzy name similarity with a proximity prior,
// and — when allowed and inconclusive — an EntityDisambiguator.
func (c *Cortex) resolve(ctx context.Context, mention, typ string, attrs map[string]any, rc *resolveContext, useLLM bool) (*Resolution, error) {
	mention = strings.TrimSpace(mention)
	if mention == "" {
		return &Resolution{}, nil
	}

	// 1. Aliases: identifiers in the mention or its attributes, then names.
	if e, err := c.matchAlias(ctx, mention, typ, attrs); err != nil || e != nil {
		return &Resolution{Entity: e, Method: ResolvedByAlias, Score: 1}, err
	}

	// 2. The owner, by self-reference or by one of the owner's names.
	if typ == "" || typ == "person" {
		ownerID, names := c.owner(ctx, rc)
		key := normalizeAlias(AliasName, mention)
		if ownerID != "" && (isSelfMention(key, rc) || names[key]) {
			e, err := c.GetEntity(ctx, ownerID)
			if err == nil {
				return &Resolution{Entity: e, Method: ResolvedByOwner, Score: 1}, nil
			}
		}
	}

	// 3. The same name ignoring case.
	if typ != "" {
		e, err := c.findSameEntity(ctx, mention, typ)
		if err != nil || e != nil {
			return &Resolution{Entity: e, Method: ResolvedByExact, Score: 1}, err
		}
	}

	// 4. Fuzzy name similarity, nudged towards entities near the owner and
	// near what this extraction has already resolved.
	cands, err := c.fuzzyCandidates(ctx, mention, typ, rc)
	if err != nil || len(cands) == 0 {
		return &Resolution{}, err
	}
	res := &Resolution{Candidates: cands}
	best := cands[0]
	runnerUp := 0.0
	if len(cands) > 1 {
		runnerUp = cands[1].Score
	}
	if best.Score >= fuzzyAutoLink && best.Score-runnerUp >= fuzzyMargin {
		e := best.Entity
		res.Entity, res.Method, res.Score = &e, ResolvedByFuzzy, best.Score
		return res, nil
	}
	if !useLLM || best.Score < fuzzyAsk {
		return res, nil
	}
	dis, ok := c.cfg.llm.(EntityDisambiguator)
	if c.cfg.llm == nil || !ok {
		return res, nil
	}
	top := cands
	if len(top) > disambiguateTopK {
		top = top[:disambiguateTopK]
	}
	ents := make([]Entity, len(top))
	for i, s := range top {
		ents[i] = s.Entity
	}
	choice, err := dis.DisambiguateEntity(ctx, EntityMention{Name: mention, Type: typ, Context: rc.content}, ents)
	if err != nil {
		c.logf("cortex: disambiguate %q: %v", mention, err)
		return res, nil
	}
	if choice.ID == "" || choice.Confidence < llmMinConfidence {
		return res, nil
	}
	for _, s := range top {
		if s.Entity.ID == choice.ID {
			e := s.Entity
			res.Entity, res.Method, res.Score = &e, ResolvedByLLM, choice.Confidence
			return res, nil
		}
	}
	return res, nil // the model named an ID that was not offered
}

// identifierAttrKeys are extracted-entity attribute keys that carry an
// identifier worth matching and remembering as an alias.
var identifierAttrKeys = map[string]string{
	"email": AliasEmail, "email_address": AliasEmail,
	"phone": AliasPhone, "phone_number": AliasPhone, "mobile": AliasPhone,
	"handle": AliasHandle, "username": AliasHandle,
}

// identifiers lists the (kind, value) identifiers in a mention and its
// attributes.
func identifiers(mention string, attrs map[string]any) [][2]string {
	var out [][2]string
	if k := DetectAliasKind(mention); k != AliasName {
		out = append(out, [2]string{k, mention})
	}
	for key, kind := range identifierAttrKeys {
		if v, ok := attrs[key].(string); ok && strings.TrimSpace(v) != "" {
			out = append(out, [2]string{kind, v})
		}
	}
	return out
}

// matchAlias finds the one live entity an alias names. Identifier aliases
// match any type; a name alias must be of the same kind as typ when typ is
// given. An alias naming several live entities is ambiguous and matches
// nothing.
func (c *Cortex) matchAlias(ctx context.Context, mention, typ string, attrs map[string]any) (*Entity, error) {
	conds := []string{"(a.kind = 'name' AND a.value_norm = ?)"}
	args := []any{c.userID, normalizeAlias(AliasName, mention)}
	for _, id := range identifiers(mention, attrs) {
		conds = append(conds, "(a.kind = ? AND a.value_norm = ?)")
		args = append(args, id[0], normalizeAlias(id[0], id[1]))
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT a.entity_id, a.kind, e.type FROM entity_aliases a
		 JOIN entities e ON e.id = a.entity_id AND e.deleted_at IS NULL
		 WHERE a.user_id = ? AND (`+strings.Join(conds, " OR ")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: match alias: %w", err)
	}
	ids := map[string]bool{}
	for rows.Next() {
		var id, kind, t string
		if err := rows.Scan(&id, &kind, &t); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cortex: scan alias match: %w", err)
		}
		if kind != AliasName || kindMatches(t, typ) {
			ids[id] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: match alias: %w", err)
	}
	if len(ids) != 1 {
		return nil, nil
	}
	for id := range ids {
		return c.GetEntity(ctx, id)
	}
	return nil, nil
}

// fuzzyCandidates scores stored entities whose names share a word with the
// mention, best first. Scores are name similarity plus proximityBoost for an
// entity one hop from the owner or from an entity this extraction resolved.
func (c *Cortex) fuzzyCandidates(ctx context.Context, mention, typ string, rc *resolveContext) ([]ScoredEntity, error) {
	var likes []string
	for _, w := range strings.Fields(strings.ToLower(mention)) {
		w = strings.Trim(w, ".,;:!?()[]{}\"'")
		if len([]rune(w)) >= 3 {
			likes = append(likes, "%"+escapeLike(w)+"%")
		}
	}
	if len(likes) == 0 {
		return nil, nil
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, type, name, confidence FROM entities
		 WHERE user_id = ? AND deleted_at IS NULL AND lower(name) LIKE ANY(?)
		 LIMIT `+fmt.Sprint(fuzzyCandidates), c.userID, likes)
	if err != nil {
		return nil, fmt.Errorf("cortex: fuzzy candidates: %w", err)
	}
	var out []ScoredEntity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Confidence); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cortex: scan candidate: %w", err)
		}
		if !kindMatches(e.Type, typ) {
			continue
		}
		if s := NameSimilarity(mention, e.Name); s > 0 {
			out = append(out, ScoredEntity{Entity: e, Score: s})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: fuzzy candidates: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}

	anchors := append([]string{}, rc.resolved...)
	if ownerID, _ := c.owner(ctx, rc); ownerID != "" {
		anchors = append(anchors, ownerID)
	}
	if near, err := c.neighbours(ctx, anchors); err == nil {
		for i := range out {
			if near[out[i].Entity.ID] {
				out[i].Score = minF(1, out[i].Score+proximityBoost)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// neighbours returns the entities one live relationship away from any of ids.
func (c *Cortex) neighbours(ctx context.Context, ids []string) (map[string]bool, error) {
	near := map[string]bool{}
	if len(ids) == 0 {
		return near, nil
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT CASE WHEN source_id = ANY(?) THEN target_id ELSE source_id END
		 FROM relationships
		 WHERE user_id = ? AND deleted_at IS NULL AND (source_id = ANY(?) OR target_id = ANY(?))`,
		ids, c.userID, ids, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		near[id] = true
	}
	return near, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// NameSimilarity scores how alike two entity names are, in [0, 1]: the
// Jaccard overlap of their character trigrams, raised to 0.88 when every word
// of a multi-word name appears in the other ("Sau Sheong" in "Chang Sau
// Sheong") and to 0.6 when a one-word name does ("Apollo" in "Apollo
// Platform") — plausible enough to ask, not enough to link on its own. Case,
// punctuation and spacing are ignored.
func NameSimilarity(a, b string) float64 {
	wa, wb := nameWords(a), nameWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	ja, jb := strings.Join(wa, " "), strings.Join(wb, " ")
	if ja == jb {
		return 1
	}
	s := jaccard(trigrams(ja), trigrams(jb))
	short, long := wa, wb
	if len(short) > len(long) {
		short, long = long, short
	}
	if wordsSubset(short, long) {
		switch {
		case len(short) >= 2 && s < 0.88:
			s = 0.88
		case len(short) == 1 && len([]rune(short[0])) >= 3 && s < 0.6:
			s = 0.6
		}
	}
	return s
}

func nameWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func wordsSubset(short, long []string) bool {
	set := make(map[string]bool, len(long))
	for _, w := range long {
		set[w] = true
	}
	for _, w := range short {
		if !set[w] {
			return false
		}
	}
	return true
}

// trigrams returns the set of padded character trigrams of s.
func trigrams(s string) map[string]struct{} {
	r := []rune("  " + s + " ")
	out := make(map[string]struct{}, len(r))
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = struct{}{}
	}
	return out
}

// DisambiguatePrompt is the system prompt LLM providers use to implement
// EntityDisambiguator; FormatDisambiguation builds the user message and
// ParseEntityChoice reads the reply.
const DisambiguatePrompt = `You decide whether a name mentioned in a text refers to one of a few known entities.
You are given the mention (name and type), the text it came from, and the candidates, each with an ID.
Pick the candidate the mention refers to, or none if it is a different entity that merely has a similar name.

Return ONLY valid JSON, no markdown:
{"id":"<candidate ID, or empty string for none>","confidence":<0.0-1.0>,"reason":"<short reason>"}`

// FormatDisambiguation renders a mention and its candidates as the user
// message for DisambiguatePrompt.
func FormatDisambiguation(m EntityMention, candidates []Entity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mention: %q (type: %s)\n\nText:\n%s\n\nCandidates:\n", m.Name, m.Type, m.Context)
	for _, e := range candidates {
		fmt.Fprintf(&b, "- ID %s: %s (%s)\n", e.ID, e.Name, e.Type)
	}
	return b.String()
}

// ParseEntityChoice parses a reply to DisambiguatePrompt, tolerating a
// markdown code fence.
func ParseEntityChoice(raw string) (EntityChoice, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var ch EntityChoice
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		return EntityChoice{}, fmt.Errorf("cortex: parse entity choice: %w", err)
	}
	return ch, nil
}
