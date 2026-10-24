package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/sausheong/cortex/internal/llmutil"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/tenancy"
	"github.com/sausheong/cortex/internal/vecmath"
	"github.com/sausheong/cortex/internal/workset"
)

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
		return model.AliasEmail
	case strings.HasPrefix(v, "@") && len(v) > 1 && !strings.ContainsAny(v, " \t"):
		return model.AliasHandle
	case isPhoneLike(v):
		return model.AliasPhone
	default:
		return model.AliasName
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
	case model.AliasPhone:
		var b strings.Builder
		for i, r := range v {
			if unicode.IsDigit(r) || (r == '+' && i == 0) {
				b.WriteRune(r)
			}
		}
		return b.String()
	case model.AliasHandle:
		return strings.ToLower(strings.TrimPrefix(v, "@"))
	default:
		return strings.ToLower(spaceRe.ReplaceAllString(v, " "))
	}
}

// NormalizeAlias is the exported comparison key of an alias value, for the
// in-memory matchers of encrypted users.
func NormalizeAlias(kind, value string) string { return normalizeAlias(kind, value) }

// AddAlias records value as an identifier of entityID. kind is one of the
// Alias* constants; empty means detect it with DetectAliasKind. Adding an
// alias the entity already has is a no-op; one that belongs to another live
// entity fails with ErrAliasTaken.
func (c Ops) AddAlias(ctx context.Context, entityID, kind, value string) error {
	if err := c.RequireKey(); err != nil {
		return err
	}
	if err := store.RequireEntity(ctx, c.DB, c.UserID, entityID); err != nil {
		return err
	}
	if kind == "" {
		kind = DetectAliasKind(value)
	}
	norm := normalizeAlias(kind, value)
	if norm == "" {
		return fmt.Errorf("cortex: empty alias")
	}
	if c.Encrypted() {
		if err := c.RequireKey(); err != nil {
			return err
		}
		return c.addAliasEnc(ctx, entityID, kind, value, norm)
	}
	var owner string
	err := c.DB.QueryRowContext(ctx,
		`INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm, source)
		 VALUES (?, ?, ?, ?, ?, 'user')
		 ON CONFLICT (user_id, kind, value_norm) WHERE enc IS NOT TRUE DO UPDATE SET
		   entity_id = CASE WHEN EXISTS (SELECT 1 FROM entities e WHERE e.id = entity_aliases.entity_id AND e.deleted_at IS NULL)
		                    THEN entity_aliases.entity_id ELSE EXCLUDED.entity_id END
		 RETURNING entity_id`,
		c.UserID, entityID, kind, strings.TrimSpace(value), norm,
	).Scan(&owner)
	if err != nil {
		return fmt.Errorf("cortex: add alias: %w", err)
	}
	if owner != entityID {
		return model.ErrAliasTaken
	}
	return nil
}

// AddAliasQuiet records an alias learned during ingest. It never moves an
// alias that already exists and never fails the ingest over one.
func (c Ops) AddAliasQuiet(ctx context.Context, entityID, kind, value, source string) {
	norm := normalizeAlias(kind, value)
	if norm == "" {
		return
	}
	if c.Encrypted() {
		if err := c.addAliasQuietEnc(ctx, entityID, kind, value, source, norm); err != nil {
			c.Logf("cortex: record alias: %v", err)
		}
		return
	}
	if _, err := c.DB.ExecContext(ctx,
		`INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm, source)
		 VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		c.UserID, entityID, kind, strings.TrimSpace(value), norm, source); err != nil {
		c.Logf("cortex: record alias %q: %v", value, err)
	}
}

// Aliases returns the identifiers recorded for an entity.
func (c Ops) Aliases(ctx context.Context, entityID string) ([]model.Alias, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT `+aliasCols+` FROM entity_aliases WHERE user_id = ? AND entity_id = ?`, c.UserID, entityID)
	if err != nil {
		return nil, fmt.Errorf("cortex: list aliases: %w", err)
	}
	defer rows.Close()
	var found []aliasRow
	for rows.Next() {
		a, err := c.scanAlias(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortAliases(found)
	var out []model.Alias
	for _, a := range found {
		out = append(out, model.Alias{EntityID: a.entityID, Kind: a.kind, Value: a.value, Source: a.source, CreatedAt: a.created})
	}
	return out, nil
}

// RemoveAlias deletes an alias. kind empty means detect it from value.
func (c Ops) RemoveAlias(ctx context.Context, kind, value string) error {
	if err := c.RequireKey(); err != nil {
		return err
	}
	if kind == "" {
		kind = DetectAliasKind(value)
	}
	norm := normalizeAlias(kind, value)
	if _, err := c.DB.ExecContext(ctx,
		`DELETE FROM entity_aliases WHERE user_id = ? AND kind = ?
		   AND ((enc AND value_blind = ?) OR (enc IS NOT TRUE AND value_norm = ?))`,
		c.UserID, kind, c.Blind(norm), norm); err != nil {
		return fmt.Errorf("cortex: remove alias: %w", err)
	}
	return nil
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
func isSelfMention(key string, rc *ResolveContext) bool {
	return userMentions[key] || (rc.OwnerSpeaks && firstPerson[key])
}

// ResolveEntity resolves a mention to the stored entity it most likely names,
// without creating anything or consulting an LLM. typ narrows name matching
// to one (normalised) type; empty matches any type. Identifier aliases
// (email, phone, handle) match regardless of type.
func (c Ops) ResolveEntity(ctx context.Context, mention, typ string) (*model.Resolution, error) {
	rc := &ResolveContext{}
	if typ != "" {
		typ, _ = ontology.CanonicalEntityType(typ)
	}
	return c.Resolve(ctx, mention, typ, nil, rc, false)
}

// ResolveContext carries what one Remember call knows while it resolves its
// entities: the source text, entities resolved so far, and the owner.
type ResolveContext struct {
	Content     string
	OwnerSpeaks bool // the text is in the owner's voice
	resolved    []string
	ownerID     string
	ownerNames  map[string]bool
	ownerReady  bool
}

// owner loads the owner entity and the names it goes by, once per context.
func (c Ops) owner(ctx context.Context, rc *ResolveContext) (string, map[string]bool) {
	if rc.ownerReady {
		return rc.ownerID, rc.ownerNames
	}
	rc.ownerReady = true
	rc.ownerNames = map[string]bool{}
	id, err := tenancy.New(c.Handle).OwnerEntityID(ctx)
	if err != nil {
		return "", rc.ownerNames
	}
	var entName, userName, email sql.NullString
	if c.Encrypted() {
		// Encrypted owner names come from the working set; the users row
		// only supplies the (plain) account name and email.
		ws, err := workset.For(ctx, c.Handle)
		if err != nil {
			return "", rc.ownerNames
		}
		e, ok := ws.EntityByID(id)
		if !ok {
			return "", rc.ownerNames
		}
		entName = sql.NullString{String: e.Name, Valid: true}
		if err := c.DB.QueryRowContext(ctx, `SELECT name, email FROM users WHERE id = ?`, c.UserID).Scan(&userName, &email); err != nil {
			return "", rc.ownerNames
		}
	} else if err := c.DB.QueryRowContext(ctx,
		`SELECT e.name, u.name, u.email FROM users u JOIN entities e ON e.id = u.owner_entity_id
		 WHERE u.id = ? AND e.deleted_at IS NULL AND e.enc IS NOT TRUE`, c.UserID).Scan(&entName, &userName, &email); err != nil {
		return "", rc.ownerNames
	}
	for _, n := range []string{entName.String, userName.String} {
		if k := normalizeAlias(model.AliasName, n); k != "" {
			rc.ownerNames[k] = true
		}
	}
	if k := normalizeAlias(model.AliasEmail, email.String); k != "" {
		rc.ownerNames[k] = true
	}
	rc.ownerID = id
	return id, rc.ownerNames
}

// resolve runs the resolution cascade: identifier and name aliases, the
// owner, an exact name, then fuzzy name similarity with a proximity prior,
// and — when allowed and inconclusive — an EntityDisambiguator.
func (c Ops) Resolve(ctx context.Context, mention, typ string, attrs map[string]any, rc *ResolveContext, useLLM bool) (*model.Resolution, error) {
	mention = strings.TrimSpace(mention)
	if mention == "" {
		return &model.Resolution{}, nil
	}

	// 1. Aliases: identifiers in the mention or its attributes, then names.
	if e, err := c.matchAlias(ctx, mention, typ, attrs); err != nil || e != nil {
		return &model.Resolution{Entity: e, Method: model.ResolvedByAlias, Score: 1}, err
	}

	// 2. The owner, by self-reference or by one of the owner's names.
	if typ == "" || typ == "person" {
		ownerID, names := c.owner(ctx, rc)
		key := normalizeAlias(model.AliasName, mention)
		if ownerID != "" && (isSelfMention(key, rc) || names[key]) {
			e, err := c.GetEntity(ctx, ownerID)
			if err == nil {
				return &model.Resolution{Entity: e, Method: model.ResolvedByOwner, Score: 1}, nil
			}
		}
	}

	// 3. The same name ignoring case.
	if typ != "" {
		e, err := c.findSameEntity(ctx, mention, typ)
		if err != nil || e != nil {
			return &model.Resolution{Entity: e, Method: model.ResolvedByExact, Score: 1}, err
		}
	}

	// 4. Fuzzy name similarity, nudged towards entities near the owner and
	// near what this extraction has already resolved.
	cands, err := c.fuzzyCandidates(ctx, mention, typ, rc)
	if err != nil || len(cands) == 0 {
		return &model.Resolution{}, err
	}
	res := &model.Resolution{Candidates: cands}
	best := cands[0]
	runnerUp := 0.0
	if len(cands) > 1 {
		runnerUp = cands[1].Score
	}
	if best.Score >= fuzzyAutoLink && best.Score-runnerUp >= fuzzyMargin {
		e := best.Entity
		res.Entity, res.Method, res.Score = &e, model.ResolvedByFuzzy, best.Score
		return res, nil
	}
	if !useLLM || best.Score < fuzzyAsk {
		return res, nil
	}
	dis, ok := c.Cfg.LLM.(model.EntityDisambiguator)
	if c.Cfg.LLM == nil || !ok {
		return res, nil
	}
	top := cands
	if len(top) > disambiguateTopK {
		top = top[:disambiguateTopK]
	}
	ents := make([]model.Entity, len(top))
	for i, s := range top {
		ents[i] = s.Entity
	}
	choice, err := dis.DisambiguateEntity(ctx, model.EntityMention{Name: mention, Type: typ, Context: rc.Content}, ents)
	if err != nil {
		c.Logf("cortex: disambiguate %q: %v", mention, err)
		return res, nil
	}
	if choice.ID == "" || choice.Confidence < llmMinConfidence {
		return res, nil
	}
	for _, s := range top {
		if s.Entity.ID == choice.ID {
			e := s.Entity
			res.Entity, res.Method, res.Score = &e, model.ResolvedByLLM, choice.Confidence
			return res, nil
		}
	}
	return res, nil // the model named an ID that was not offered
}

// identifierAttrKeys are extracted-entity attribute keys that carry an
// identifier worth matching and remembering as an alias.
var identifierAttrKeys = map[string]string{
	"email": model.AliasEmail, "email_address": model.AliasEmail,
	"phone": model.AliasPhone, "phone_number": model.AliasPhone, "mobile": model.AliasPhone,
	"handle": model.AliasHandle, "username": model.AliasHandle,
}

// identifiers lists the (kind, value) identifiers in a mention and its
// attributes.
func identifiers(mention string, attrs map[string]any) [][2]string {
	var out [][2]string
	if k := DetectAliasKind(mention); k != model.AliasName {
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
func (c Ops) matchAlias(ctx context.Context, mention, typ string, attrs map[string]any) (*model.Entity, error) {
	var conds []string
	var args []any
	legacy := c.legacyOK()
	add := func(kind, norm string) {
		conds = append(conds, `(a.kind = ? AND ((a.enc AND a.value_blind = ?) OR (?::boolean AND a.enc IS NOT TRUE AND a.value_norm = ?)))`)
		args = append(args, kind, c.Blind(norm), legacy, norm)
	}
	add(model.AliasName, normalizeAlias(model.AliasName, mention))
	for _, id := range identifiers(mention, attrs) {
		add(id[0], normalizeAlias(id[0], id[1]))
	}
	rows, err := c.DB.QueryContext(ctx,
		`SELECT a.id, a.entity_id, a.kind, a.enc, a.value, a.source, a.value_ct, a.source_ct, a.created_at,
		        e.id, e.enc, e.type, e.type_ct
		 FROM entity_aliases a
		 JOIN entities e ON e.id = a.entity_id AND e.deleted_at IS NULL
		 WHERE a.user_id = ? AND (`+strings.Join(conds, " OR ")+`)`, append([]any{c.UserID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("cortex: match alias: %w", err)
	}
	ids := map[string]bool{}
	var legacyHits []aliasRow
	for rows.Next() {
		var entID string
		var entEnc bool
		var entType sql.NullString
		var typeCT []byte
		var a aliasRow
		var value, source sql.NullString
		var valueCT, sourceCT []byte
		if err := rows.Scan(&a.id, &a.entityID, &a.kind, &a.enc, &value, &source, &valueCT, &sourceCT, &a.created,
			&entID, &entEnc, &entType, &typeCT); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cortex: scan alias match: %w", err)
		}
		if err := c.CheckRowForm(a.enc); err != nil {
			rows.Close()
			return nil, err
		}
		t, err := c.entityType(entID, entEnc, entType, typeCT)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if a.kind != model.AliasName || kindMatches(t, typ) {
			ids[a.entityID] = true
			if !a.enc {
				a.value, a.source = value.String, source.String
				legacyHits = append(legacyHits, a)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: match alias: %w", err)
	}
	if c.Migrating() && c.Key != nil {
		for _, a := range legacyHits {
			if err := c.writeAliasEnc(ctx, c.DB, a.id, a.entityID, a.kind, a.value, a.source); err != nil {
				c.Logf("cortex: rewrite legacy alias: %v", err)
			} else {
				c.NotifyWrite("alias")
			}
		}
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
func (c Ops) fuzzyCandidates(ctx context.Context, mention, typ string, rc *ResolveContext) ([]model.ScoredEntity, error) {
	var likes, words []string
	for _, w := range strings.Fields(strings.ToLower(mention)) {
		w = strings.Trim(w, ".,;:!?()[]{}\"'")
		if len([]rune(w)) >= 3 {
			words = append(words, w)
			likes = append(likes, "%"+llmutil.EscapeLike(w)+"%")
		}
	}
	if len(likes) == 0 {
		return nil, nil
	}
	var out []model.ScoredEntity
	if c.Encrypted() {
		// Postgres cannot match words inside ciphertext: candidates are
		// picked over the decrypted working set with the same word filter
		// as the SQL path's LIKE.
		ws, err := workset.For(ctx, c.Handle)
		if err != nil {
			return nil, err
		}
		matched := 0
		for _, e := range ws.Entities() {
			if matched >= fuzzyCandidates {
				break
			}
			low := strings.ToLower(e.Name)
			hit := false
			for _, w := range words {
				if strings.Contains(low, w) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			matched++
			if !kindMatches(e.Type, typ) {
				continue
			}
			if s := NameSimilarity(mention, e.Name); s > 0 {
				out = append(out, model.ScoredEntity{Entity: e, Score: s})
			}
		}
	} else {
		var err error
		if out, err = c.fuzzyLegacy(ctx, mention, typ, likes); err != nil {
			return nil, err
		}
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

// fuzzyLegacy is the SQL candidate filter for unencrypted users.
func (c Ops) fuzzyLegacy(ctx context.Context, mention, typ string, likes []string) ([]model.ScoredEntity, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT id, enc, type, name, type_ct, name_ct, confidence FROM entities
		 WHERE user_id = ? AND deleted_at IS NULL AND enc IS NOT TRUE AND lower(name) LIKE ANY(?)
		 `, c.UserID, likes)
	if err != nil {
		return nil, fmt.Errorf("cortex: fuzzy candidates: %w", err)
	}
	var out []model.ScoredEntity
	matched := 0
	for rows.Next() {
		var e model.Entity
		var enc bool
		var etyp, name sql.NullString
		var typeCT, nameCT []byte
		if err := rows.Scan(&e.ID, &enc, &etyp, &name, &typeCT, &nameCT, &e.Confidence); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cortex: scan candidate: %w", err)
		}
		if matched >= fuzzyCandidates {
			break
		}
		if err := c.openNameType(&e, enc, name, etyp, nameCT, typeCT); err != nil {
			rows.Close()
			return nil, err
		}
		matched++
		if !kindMatches(e.Type, typ) {
			continue
		}
		if s := NameSimilarity(mention, e.Name); s > 0 {
			out = append(out, model.ScoredEntity{Entity: e, Score: s})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: fuzzy candidates: %w", err)
	}
	return out, nil
}

// neighbours returns the entities one live relationship away from any of ids.
func (c Ops) neighbours(ctx context.Context, ids []string) (map[string]bool, error) {
	near := map[string]bool{}
	if len(ids) == 0 {
		return near, nil
	}
	rows, err := c.DB.QueryContext(ctx,
		`SELECT CASE WHEN source_id = ANY(?) THEN target_id ELSE source_id END
		 FROM relationships
		 WHERE user_id = ? AND deleted_at IS NULL AND (source_id = ANY(?) OR target_id = ANY(?))`,
		ids, c.UserID, ids, ids)
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
	s := vecmath.Jaccard(trigrams(ja), trigrams(jb))
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
func FormatDisambiguation(m model.EntityMention, candidates []model.Entity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mention: %q (type: %s)\n\nText:\n%s\n\nCandidates:\n", m.Name, m.Type, m.Context)
	for _, e := range candidates {
		fmt.Fprintf(&b, "- ID %s: %s (%s)\n", e.ID, e.Name, e.Type)
	}
	return b.String()
}

// ParseEntityChoice parses a reply to DisambiguatePrompt, tolerating a
// markdown code fence.
func ParseEntityChoice(raw string) (model.EntityChoice, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var ch model.EntityChoice
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		return model.EntityChoice{}, fmt.Errorf("cortex: parse entity choice: %w", err)
	}
	return ch, nil
}
