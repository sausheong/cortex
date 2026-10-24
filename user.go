package cortex

// UserID returns the ID of the user this handle is bound to.
func (c *Cortex) UserID() string { return c.h.UserID }

// ForUser returns a handle bound to another user. It shares the database
// connection but has its own configuration; opts override the LLM, embedder
// and extractor.
func (c *Cortex) ForUser(id string, opts ...Option) *Cortex {
	h2 := *c.h
	h2.UserID = id
	for _, o := range opts {
		o(&h2.Cfg)
	}
	return &Cortex{h: &h2}
}
