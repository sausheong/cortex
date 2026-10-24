// Package cortex is a persistent knowledge graph and memory store for LLM
// agents, backed by Postgres with pgvector.
//
// It keeps what an agent learns as memories, entities and relationships,
// scoped per user. Open connects and migrates the database; ForUser returns a
// handle bound to another user that shares the connection:
//
//	c, err := cortex.Open(dsn, cortex.WithEmbedder(e), cortex.WithExtractor(x))
//	if err != nil { ... }
//	defer c.Close()
//	alice := c.ForUser("alice")
//
// Remember stores text (extracting entities and relationships when an
// extractor is configured) and Recall retrieves relevant memories by
// combining full-text, vector and graph signals:
//
//	err = alice.Remember(ctx, "Bob joined Acme as CTO in March")
//	res, err := alice.Recall(ctx, "who is Bob?")
//
// The implementation lives under internal/; this package is the public API.
// See the README for the architecture, deployment and command line tools.
package cortex
