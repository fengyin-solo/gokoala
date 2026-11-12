package domain

// Expansion explains how the original search input was normalized, rewritten and
// expanded with synonyms. It only contains user input and configured rewrite/synonym
// information: no SQL, datasource queries or other database internals are exposed.
// All slices use a deterministic order and a bounded number of entries.
type Expansion struct {
	// OriginalInput as received, after basic normalization (trimming and lowercasing).
	OriginalInput string `json:"originalInput"`
	// NormalizedInput is the input after all configured rewrites were applied.
	NormalizedInput string `json:"normalizedInput"`
	// Words in the order in which they first appear in the normalized input.
	Words []string `json:"words"`
	// Rewrites that actually changed the input, in the order in which they were applied.
	Rewrites []RewriteStep `json:"rewrites,omitempty"`
	// Synonyms expanded per word, in word order. Words without synonyms are omitted.
	Synonyms []WordSynonyms `json:"synonyms,omitempty"`
	// WordsWithoutSynonyms, in word order.
	WordsWithoutSynonyms []string `json:"wordsWithoutSynonyms,omitempty"`
	// TruncatedWords are words for which synonym expansion was cut off because a
	// configured limit was reached, so not every possible synonym is included.
	TruncatedWords []string `json:"truncatedWords,omitempty"`
}

// RewriteStep describes a single configured rewrite that was applied during normalization.
type RewriteStep struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// WordSynonyms lists the synonyms generated for a single word.
type WordSynonyms struct {
	Word string `json:"word"`
	// Synonyms in deterministic (lexicographic) order, capped to a maximum number of entries.
	Synonyms []string `json:"synonyms"`
	// Truncated is true when not all generated synonyms could be included due to a limit.
	Truncated bool `json:"truncated,omitempty"`
}
