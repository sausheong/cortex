package model

import (
	"errors"
	"fmt"
)

// ErrAliasTaken is returned by AddAlias when the alias already belongs to a
// different entity.
var ErrAliasTaken = errors.New("cortex: alias already belongs to another entity")

// ErrEntityNameTaken is returned by RenameEntity when another live entity of
// the same type already has the name. ConflictID is that entity.
type ErrEntityNameTaken struct{ ConflictID string }

func (e *ErrEntityNameTaken) Error() string {
	return "cortex: an entity with that name and type exists"
}

// ErrNotFound is returned for an absent ID and, identically, for an ID that
// belongs to another user.
var ErrNotFound = errors.New("cortex: not found")

// NotFoundError is the error behind ErrNotFound; it carries a descriptive
// message and matches ErrNotFound under errors.Is.
type NotFoundError struct{ msg string }

func (e *NotFoundError) Error() string { return e.msg }

func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// NotFound builds an error that wraps ErrNotFound with a formatted message.
func NotFound(format string, args ...any) error {
	return &NotFoundError{msg: fmt.Sprintf(format, args...)}
}

// ErrStaleGeneration is returned when a handle pinned to an embedding
// generation writes after the user's active generation has moved on.
var ErrStaleGeneration = errors.New("cortex: embedding generation is stale")

// EmbedPartialError reports that Remember stored everything but could not
// embed some items. Keyword search finds them now; nightly maintenance
// backfills the vectors so semantic search catches up.
type EmbedPartialError struct {
	Count int
	Err   error // the first embedding failure
}

func (e *EmbedPartialError) Error() string {
	return fmt.Sprintf("cortex: saved, but %d item(s) are waiting for an embedding (retried nightly): %v", e.Count, e.Err)
}

func (e *EmbedPartialError) Unwrap() error { return e.Err }

// ExtractPartialError reports that Remember stored what it could but the
// extractor failed on some sections of a long document. The raw text of every
// section is still saved and keyword-searchable; only the entities,
// relationships and memories drawn from the failed sections are missing.
type ExtractPartialError struct {
	Failed int
	Total  int
	Err    error // the first extraction failure
}

func (e *ExtractPartialError) Error() string {
	return fmt.Sprintf("cortex: saved, but %d of %d section(s) could not be analysed for entities and facts: %v", e.Failed, e.Total, e.Err)
}

func (e *ExtractPartialError) Unwrap() error { return e.Err }

// Encryption errors. One wording each, shared by every layer.
var (
	ErrLocked              = errors.New("cortex: locked; unlock with your passkey")
	ErrNeedsReconnect      = errors.New("cortex is now encrypted; reconnect this assistant in Connections")
	ErrTokenExpired        = errors.New("cortex: token expired; reconnect this assistant in Connections")
	ErrEnrollRequired      = errors.New("cortex: encryption is required; set up your passkey in Connections")
	ErrStaticTokenDisabled = errors.New("the static owner token is disabled for encrypted users; mint an agent token in Connections")
	ErrPlaintextRow        = errors.New("cortex: plaintext row in an encrypted account")
)
