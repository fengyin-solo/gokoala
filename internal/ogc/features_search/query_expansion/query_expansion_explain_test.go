package query_expansion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	fdomain "github.com/PDOK/gokoala/internal/ogc/features/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestQueryExpansion(t *testing.T, maxSynonyms int) *QueryExpansion {
	t.Helper()
	queryExpansion, err := NewQueryExpansion(
		"internal/ogc/features_search/testdata/rewrites.csv",
		"internal/ogc/features_search/testdata/synonyms.csv",
		maxSynonyms)
	require.NoError(t, err)
	return queryExpansion
}

func TestExpandExplanationWithoutAnyExpansion(t *testing.T) {
	qe := newTestQueryExpansion(t, 100)

	_, expansion, err := qe.Expand(context.Background(), "just some text")
	require.NoError(t, err)

	assert.Equal(t, "just some text", expansion.OriginalInput)
	assert.Equal(t, "just some text", expansion.NormalizedInput)
	assert.Equal(t, []string{"just", "some", "text"}, expansion.Words)
	assert.Empty(t, expansion.Rewrites)
	assert.Empty(t, expansion.Synonyms)
	assert.Empty(t, expansion.TruncatedWords)
	assert.Equal(t, []string{"just", "some", "text"}, expansion.WordsWithoutSynonyms)
}

func TestExpandExplanationSingleRewrite(t *testing.T) {
	qe := newTestQueryExpansion(t, 100)

	_, expansion, err := qe.Expand(context.Background(), "markt den bosch")
	require.NoError(t, err)

	assert.Equal(t, "markt den bosch", expansion.OriginalInput)
	assert.Equal(t, "markt hertogenbosch", expansion.NormalizedInput)
	assert.Equal(t, []string{"markt", "hertogenbosch"}, expansion.Words)
	require.Len(t, expansion.Rewrites, 1)
	assert.Equal(t, fdomain.RewriteStep{From: "den bosch", To: "hertogenbosch"}, expansion.Rewrites[0])
	assert.Empty(t, expansion.Synonyms)
	assert.Equal(t, []string{"markt", "hertogenbosch"}, expansion.WordsWithoutSynonyms)
	assert.Empty(t, expansion.TruncatedWords)
}

func TestExpandExplanationRegexRewrite(t *testing.T) {
	qe := newTestQueryExpansion(t, 100)

	_, expansion, err := qe.Expand(context.Background(), "dorpstraat 5 6784 gx village")
	require.NoError(t, err)

	assert.Equal(t, "dorpstraat 5 6784gx village", expansion.NormalizedInput)
	require.Len(t, expansion.Rewrites, 1)
	// the internal "regex:" marker is not exposed, the configured pattern itself is
	// (rewrites are lowercased when loaded)
	assert.Equal(t, "^([0-9]{4}) ([a-za-z]{2})$", expansion.Rewrites[0].From)
	assert.Equal(t, "${1}${2}", expansion.Rewrites[0].To)
	assert.Equal(t, []string{"dorpstraat", "5", "6784gx", "village"}, expansion.Words)
}

func TestExpandExplanationMultipleWordsWithSynonyms(t *testing.T) {
	qe := newTestQueryExpansion(t, 100)

	_, expansion, err := qe.Expand(context.Background(), "eerste 2de")
	require.NoError(t, err)

	assert.Equal(t, "eerste 2de", expansion.OriginalInput)
	assert.Equal(t, "eerste 2de", expansion.NormalizedInput)
	assert.Empty(t, expansion.Rewrites)
	assert.Empty(t, expansion.WordsWithoutSynonyms)
	require.Len(t, expansion.Synonyms, 2)
	// entries follow the original word order
	assert.Equal(t, "eerste", expansion.Synonyms[0].Word)
	assert.Equal(t, []string{"1ste"}, expansion.Synonyms[0].Synonyms)
	assert.False(t, expansion.Synonyms[0].Truncated)
	assert.Equal(t, "2de", expansion.Synonyms[1].Word)
	assert.Equal(t, []string{"tweede"}, expansion.Synonyms[1].Synonyms)
	assert.Empty(t, expansion.TruncatedWords)
}

func TestExpandExplanationOverlappingSynonyms(t *testing.T) {
	qe := newTestQueryExpansion(t, 100)

	_, expansion, err := qe.Expand(context.Background(), "foosball")
	require.NoError(t, err)

	require.Len(t, expansion.Synonyms, 1)
	assert.Equal(t, "foosball", expansion.Synonyms[0].Word)
	// deterministic lexicographic order, overlapping matches are included
	assert.Equal(t, []string{"fooball", "foobarball"}, expansion.Synonyms[0].Synonyms)
	assert.False(t, expansion.Synonyms[0].Truncated)
}

func TestExpandExplanationTruncation(t *testing.T) {
	qe := newTestQueryExpansion(t, 3)

	_, expansion, err := qe.Expand(context.Background(), "oudwesterlijke-goeverneur")
	require.NoError(t, err)

	require.Len(t, expansion.Synonyms, 1)
	entry := expansion.Synonyms[0]
	assert.Equal(t, "oudwesterlijke-goeverneur", entry.Word)
	// only the configured maximum number of synonyms is exposed
	assert.Len(t, entry.Synonyms, 3)
	assert.True(t, entry.Truncated)
	assert.Equal(t, []string{
		"oudwesterlijke-goev",
		"oudwesterlijke-gouv",
		"oudwesterlijke-gouverneur",
	}, entry.Synonyms)
	assert.Equal(t, []string{"oudwesterlijke-goeverneur"}, expansion.TruncatedWords)
}

func TestExpandExplanationWordsRemainInOriginalOrder(t *testing.T) {
	qe := newTestQueryExpansion(t, 100)

	_, expansion, err := qe.Expand(context.Background(), "ok text with spaces ok")
	require.NoError(t, err)

	// duplicates are removed, first-occurrence order is preserved
	assert.Equal(t, []string{"ok", "text", "with", "spaces"}, expansion.Words)
	assert.Equal(t, []string{"ok", "text", "with", "spaces"}, expansion.WordsWithoutSynonyms)
}

func TestExpandExplanationIsDeterministicAndContainsNoDatabaseInternals(t *testing.T) {
	qe := newTestQueryExpansion(t, 100)
	input := "Oude Piet Westgouverneurstraat 1800"

	_, first, err := qe.Expand(context.Background(), input)
	require.NoError(t, err)
	_, second, err := qe.Expand(context.Background(), input)
	require.NoError(t, err)

	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)
	assert.JSONEq(t, string(firstJSON), string(secondJSON))
	assert.Equal(t, string(firstJSON), string(secondJSON))

	// no SQL or datasource internals may leak into the explanation
	for _, forbidden := range []string{"select", "tsquery", "to_tsquery", "from ", "where "} {
		assert.False(t, strings.Contains(strings.ToLower(string(firstJSON)), forbidden),
			"explanation should not contain %q", forbidden)
	}
}
