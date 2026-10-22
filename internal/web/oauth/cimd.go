package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
)

var errInvalidClient = errors.New("oauth: invalid client")

type client struct {
	ID, Kind, Name, CIMDHost string
	RedirectURIs             []string
	AuthorizedAt             *time.Time
}

func (c *client) hasRedirect(u string) bool {
	for _, x := range c.RedirectURIs {
		if x == u {
			return true
		}
	}
	return false
}

// loadClient resolves a client_id (DCR row or CIMD URL) to a usable client.
func (h *handler) loadClient(ctx context.Context, id string) (*client, error) {
	if id == "" || len(id) > 2048 {
		return nil, errInvalidClient
	}
	now := h.now().UTC()
	if strings.HasPrefix(id, "https://") {
		return h.loadCIMD(ctx, id, now)
	}
	var c client
	var uris []byte
	var authAt, expAt sql.NullTime
	err := h.st.DB.QueryRowContext(ctx,
		`SELECT id, kind, name, redirect_uris, cimd_host, authorized_at, expires_at FROM oauth_clients WHERE id = $1 AND kind = 'dcr'`, id).
		Scan(&c.ID, &c.Kind, &c.Name, &uris, &c.CIMDHost, &authAt, &expAt)
	if err != nil {
		return nil, errInvalidClient
	}
	if !authAt.Valid && expAt.Valid && !expAt.Time.After(now) {
		return nil, errInvalidClient
	}
	if authAt.Valid {
		t := authAt.Time
		c.AuthorizedAt = &t
	}
	if json.Unmarshal(uris, &c.RedirectURIs) != nil {
		return nil, errInvalidClient
	}
	return &c, nil
}

func (h *handler) loadCIMD(ctx context.Context, id string, now time.Time) (*client, error) {
	u, err := url.Parse(id)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Fragment != "" || u.User != nil || u.Path == "" || u.Path == "/" {
		return nil, errInvalidClient
	}
	var c client
	var uris []byte
	var exp sql.NullTime
	var authAt sql.NullTime
	err = h.st.DB.QueryRowContext(ctx,
		`SELECT id, kind, name, redirect_uris, cimd_host, authorized_at, expires_at FROM oauth_clients WHERE id = $1 AND kind = 'cimd'`, id).
		Scan(&c.ID, &c.Kind, &c.Name, &uris, &c.CIMDHost, &authAt, &exp)
	if err == nil && exp.Valid && exp.Time.After(now) && json.Unmarshal(uris, &c.RedirectURIs) == nil {
		if authAt.Valid {
			t := authAt.Time
			c.AuthorizedAt = &t
		}
		return &c, nil
	}

	hc := h.o.CIMDClient
	if hc == nil {
		hc = accounts.SafeClient(false, accounts.CIMDBodyCap)
	}
	to := h.o.CIMDTimeout
	if to == 0 {
		to = 5 * time.Second
	}
	fctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, id, nil)
	if err != nil {
		return nil, errInvalidClient
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, errInvalidClient
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errInvalidClient
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, accounts.CIMDBodyCap+1))
	if err != nil || len(body) > accounts.CIMDBodyCap {
		return nil, errInvalidClient
	}
	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.ClientID != id || len(doc.RedirectURIs) == 0 || len(doc.RedirectURIs) > 10 {
		return nil, errInvalidClient
	}
	for _, r := range doc.RedirectURIs {
		if !validRedirect(r) {
			return nil, errInvalidClient
		}
	}
	name := strings.TrimSpace(doc.ClientName)
	if name == "" {
		name = u.Host
	}
	if len(name) > 100 {
		name = name[:100]
	}
	c = client{ID: id, Kind: "cimd", Name: name, CIMDHost: u.Host, RedirectURIs: doc.RedirectURIs}
	raw, _ := json.Marshal(doc.RedirectURIs)
	if _, err := h.st.DB.ExecContext(ctx,
		`INSERT INTO oauth_clients (id, kind, name, redirect_uris, cimd_host, created_at, expires_at) VALUES ($1,'cimd',$2,$3,$4,$5,$6)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, redirect_uris = EXCLUDED.redirect_uris,
		 cimd_host = EXCLUDED.cimd_host, expires_at = EXCLUDED.expires_at`,
		id, name, raw, u.Host, now, now.Add(cimdCacheTTL)); err != nil {
		return nil, err
	}
	return &c, nil
}
