package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
)

// Table names used as AAD components. They are the real table names.
const (
	TblEntities      = "entities"
	TblRelationships = "relationships"
	TblAliases       = "entity_aliases"
	TblMemories      = "memories"
	TblMemoryEdges   = "memory_edges"
	TblChunks        = "chunks"
	TblEmbeddings    = "embeddings"
)

// OpenEntityFields fills e (whose ID is set) from one entities row of either
// form. Legacy rows pass through the row-form guard; enc rows are decrypted,
// and a NULL ciphertext fails.
func (h *Handle) OpenEntityFields(e *model.Entity, enc bool, name, typ, source, attrs sql.NullString, nameCT, typeCT, sourceCT, attrsCT []byte) error {
	if err := h.CheckRowForm(enc); err != nil {
		return err
	}
	if !enc {
		e.Name, e.Type, e.Source = name.String, typ.String, source.String
		if attrs.Valid && attrs.String != "" {
			if err := json.Unmarshal([]byte(attrs.String), &e.Attributes); err != nil {
				return fmt.Errorf("cortex: unmarshal attributes: %w", err)
			}
		}
		return nil
	}
	var err error
	if e.Name, err = h.OpenStr(TblEntities, e.ID, "name", nameCT); err != nil {
		return err
	}
	if e.Type, err = h.OpenStr(TblEntities, e.ID, "type", typeCT); err != nil {
		return err
	}
	if e.Source, err = h.OpenStr(TblEntities, e.ID, "source", sourceCT); err != nil {
		return err
	}
	return h.OpenJSON(TblEntities, e.ID, "attributes", attrsCT, &e.Attributes)
}

// OpenRelFields is OpenEntityFields for relationships rows.
func (h *Handle) OpenRelFields(r *model.Relationship, enc bool, typ, source, attrs sql.NullString, typeCT, sourceCT, attrsCT []byte) error {
	if err := h.CheckRowForm(enc); err != nil {
		return err
	}
	if !enc {
		r.Type, r.Source = typ.String, source.String
		if attrs.Valid && attrs.String != "" {
			if err := json.Unmarshal([]byte(attrs.String), &r.Attributes); err != nil {
				return fmt.Errorf("cortex: unmarshal relationship attributes: %w", err)
			}
		}
		return nil
	}
	var err error
	if r.Type, err = h.OpenStr(TblRelationships, r.ID, "type", typeCT); err != nil {
		return err
	}
	if r.Source, err = h.OpenStr(TblRelationships, r.ID, "source", sourceCT); err != nil {
		return err
	}
	return h.OpenJSON(TblRelationships, r.ID, "attributes", attrsCT, &r.Attributes)
}

// OpenMemoryFields fills content, source and speaker of m (ID set).
func (h *Handle) OpenMemoryFields(m *model.Memory, enc bool, content, source, speaker sql.NullString, contentCT, sourceCT, speakerCT []byte) error {
	if err := h.CheckRowForm(enc); err != nil {
		return err
	}
	if !enc {
		m.Content, m.Source, m.Speaker = content.String, source.String, speaker.String
		return nil
	}
	var err error
	if m.Content, err = h.OpenStr(TblMemories, m.ID, "content", contentCT); err != nil {
		return err
	}
	if m.Source, err = h.OpenStr(TblMemories, m.ID, "source", sourceCT); err != nil {
		return err
	}
	m.Speaker, err = h.OpenOptStr(TblMemories, m.ID, "speaker", speakerCT)
	return err
}
