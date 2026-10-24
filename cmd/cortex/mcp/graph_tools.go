package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
)

// registerGraphTools adds the curated graph queries and the structured
// writes (aliases, explicit relationships).
func registerGraphTools(s *server.MCPServer, h handles) {
	// --- who_is ---
	s.AddTool(
		mcp.NewTool("who_is",
			mcp.WithDescription("Look up a person, organisation or anything else by name, alias, email, phone or handle. Returns the entity with its aliases, current relationships and memories. If the name is ambiguous, returns the closest candidates instead."),
			mcp.WithString("name", mcp.Required(), mcp.Description("A name, alias, email address, phone number or @handle")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			card, err := cx.WhoIs(ctx, name)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(card)
		},
	)

	// --- connections ---
	s.AddTool(
		mcp.NewTool("connections",
			mcp.WithDescription("Find how two entities are connected: the shortest chain of current relationships between them. Each end may be an entity ID or a name."),
			mcp.WithString("from", mcp.Required(), mcp.Description("Entity ID or name")),
			mcp.WithString("to", mcp.Required(), mcp.Description("Entity ID or name")),
			mcp.WithNumber("max_hops", mcp.Description("Longest chain to consider (default 4, max 6)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			from, err := entityRef(ctx, cx, req, "from")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			to, err := entityRef(ctx, cx, req, "to")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			path, err := cx.Path(ctx, from, to, req.GetInt("max_hops", 0))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out := struct {
				Connected bool               `json:"connected"`
				Path      []cortex.NamedEdge `json:"path"`
				Summary   []string           `json:"summary"`
			}{Connected: path != nil, Path: path, Summary: []string{}}
			if out.Path == nil {
				out.Path = []cortex.NamedEdge{}
			}
			for _, e := range path {
				out.Summary = append(out.Summary, e.String())
			}
			return jsonResult(out)
		},
	)

	// --- timeline ---
	s.AddTool(
		mcp.NewTool("timeline",
			mcp.WithDescription("An entity's history, oldest first: its memories (including ones no longer true) and when its relationships started and ended."),
			mcp.WithString("entity", mcp.Required(), mcp.Description("Entity ID or name")),
			mcp.WithString("since", mcp.Description("Optional RFC3339 timestamp; drop earlier events")),
			mcp.WithNumber("limit", mcp.Description("Most recent events to return (default 100)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			id, err := entityRef(ctx, cx, req, "entity")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var since time.Time
			if s := req.GetString("since", ""); s != "" {
				if since, err = time.Parse(time.RFC3339, s); err != nil {
					return mcp.NewToolResultError("since must be an RFC3339 timestamp"), nil
				}
			}
			events, err := cx.Timeline(ctx, id, since, req.GetInt("limit", 0))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(events)
		},
	)

	// --- what_changed ---
	s.AddTool(
		mcp.NewTool("what_changed",
			mcp.WithDescription("What was added to or retired from the knowledge graph since a time: new entities, memories and relationships, memories no longer current, and relationships that ended."),
			mcp.WithString("since", mcp.Required(), mcp.Description("RFC3339 timestamp")),
			mcp.WithNumber("limit", mcp.Description("Most items per list (default 50)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			s, err := req.RequireString("since")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			since, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return mcp.NewToolResultError("since must be an RFC3339 timestamp"), nil
			}
			ch, err := cx.WhatChanged(ctx, since, req.GetInt("limit", 0))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(ch)
		},
	)

	// --- add_alias ---
	s.AddTool(
		mcp.NewTool("add_alias",
			mcp.WithDescription("Record another identifier for an entity — an email address, phone number, @handle or alternative name — so later mentions of it resolve to that entity. Use when you know two names refer to the same person or thing."),
			mcp.WithString("entity", mcp.Required(), mcp.Description("Entity ID or name")),
			mcp.WithString("value", mcp.Required(), mcp.Description("The identifier or alternative name")),
			mcp.WithString("kind", mcp.Description("name, email, phone or handle (default: detected from value)"), mcp.Enum("name", "email", "phone", "handle")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			id, err := entityRef(ctx, cx, req, "entity")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			value, err := req.RequireString("value")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := cx.AddAlias(ctx, id, req.GetString("kind", ""), value); err != nil {
				if errors.Is(err, cortex.ErrAliasTaken) {
					return mcp.NewToolResultError("that identifier already belongs to another entity; merge the two entities instead"), nil
				}
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("alias recorded"), nil
		},
	)

	// --- link_entities ---
	s.AddTool(
		mcp.NewTool("link_entities",
			mcp.WithDescription("Record a relationship you know for certain (e.g. from an email header or a calendar invite) without free-text extraction. Ends are entity IDs or names; a name that matches nothing creates an entity of the given type. The type is mapped onto cortex's relationship vocabulary (works_at, member_of, knows, communicated_with, lives_in, ...)."),
			mcp.WithString("source", mcp.Required(), mcp.Description("Entity ID or name at the start of the relationship")),
			mcp.WithString("type", mcp.Required(), mcp.Description("Relationship type, read source-type-target, e.g. works_at")),
			mcp.WithString("target", mcp.Required(), mcp.Description("Entity ID or name at the end")),
			mcp.WithString("source_type", mcp.Description("Entity type if the source must be created (default person)")),
			mcp.WithString("target_type", mcp.Description("Entity type if the target must be created (default person)")),
			mcp.WithString("provenance", mcp.Description("Optional source label, e.g. gmail")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			src, err := req.RequireString("source")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			typ, err := req.RequireString("type")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			tgt, err := req.RequireString("target")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rel, err := cx.Link(ctx, cortex.LinkRequest{
				Source: src, SourceType: req.GetString("source_type", ""),
				Target: tgt, TargetType: req.GetString("target_type", ""),
				Type: typ, Provenance: req.GetString("provenance", ""),
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(rel)
		},
	)
}

// entityRef reads a required parameter naming an entity by ID or by name
// and returns the entity's ID. An ID that does not exist for this user and
// a name that does not resolve fail with the same message, so a foreign ID
// is indistinguishable from an absent one.
func entityRef(ctx context.Context, cx *cortex.Cortex, req mcp.CallToolRequest, param string) (string, error) {
	ref, err := req.RequireString(param)
	if err != nil {
		return "", err
	}
	if e, err := cx.GetEntity(ctx, ref); err == nil {
		return e.ID, nil
	}
	res, err := cx.ResolveEntity(ctx, ref, "")
	if err != nil {
		return "", err
	}
	if res.Entity == nil {
		return "", fmt.Errorf("%s: no entity matches %q", param, ref)
	}
	return res.Entity.ID, nil
}
