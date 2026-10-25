package model

// Compaction change kinds.
const (
	CompactMerge   = "merge"
	CompactCombine = "combine"
	CompactRollup  = "rollup"
)

// Alias kinds. A name alias is another name for the entity; the others are
// identifiers that name exactly one entity regardless of its type.
const (
	AliasName   = "name"
	AliasEmail  = "email"
	AliasPhone  = "phone"
	AliasHandle = "handle"
)

// Resolution methods, in the order ingest tries them.
const (
	ResolvedByAlias = "alias"
	ResolvedByOwner = "owner"
	ResolvedByExact = "exact"
	ResolvedByFuzzy = "fuzzy"
	ResolvedByLLM   = "llm"
)

// BootstrapUserID is the user that single-user library use and every
// pre-multi-user row belongs to.
const BootstrapUserID = "u_bootstrap"

// NoConnection is the MemoryListFilter.WrittenBy value for memories that no
// connection wrote.
const NoConnection = "none"

// DefaultMaxChunkChars is the chunk size Remember uses unless told
// otherwise: about 1,500 tokens of English, well inside the input limit of
// common embedding models (OpenAI text-embedding-3-*, nomic, bge).
const DefaultMaxChunkChars = 6000

// SourceVaultCorrection is the Source of a memory the user wrote as a
// correction inside the Vault. Its WrittenBy stays empty: WrittenBy is a
// connection id, and a correction is not written by any connection.
const SourceVaultCorrection = "vault correction"
