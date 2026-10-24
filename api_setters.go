package cortex

// SetExtractor sets the extractor on the Cortex instance.
func (c *Cortex) SetExtractor(e Extractor) { c.h.Cfg.Extractor = e }

// SetLLM sets the LLM on the Cortex instance.
func (c *Cortex) SetLLM(l LLM) { c.h.Cfg.LLM = l }

// SetEmbedder sets the embedder on the Cortex instance.
func (c *Cortex) SetEmbedder(e Embedder) { c.h.Cfg.Embedder = e }
