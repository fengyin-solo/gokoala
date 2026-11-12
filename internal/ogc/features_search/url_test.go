package features_search

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSearchTermsRejectsOperators(t *testing.T) {
	tests := []string{
		"foo|bar",
		"!foo",
		"foo<->bar",
		"foo<2>bar",
	}

	for _, searchTerms := range tests {
		t.Run(searchTerms, func(t *testing.T) {
			actual, err := parseSearchTerms(url.Values{queryParam: {searchTerms}})

			assert.Empty(t, actual)
			require.EqualError(t, err, "provided search terms contain one ore more boolean operators which aren't allowed")
		})
	}
}

func TestParseSearchTermsRejectsInvalidCharacters(t *testing.T) {
	tests := []struct {
		searchTerms   string
		expectedError string
	}{
		{"foo:bar", "provided search terms contain invalid character ':'"},
		{"foo*bar", "provided search terms contain invalid character '*'"},
		{"foo<bar", "provided search terms contain invalid character '<'"},
		{"foo>bar", "provided search terms contain invalid character '>'"},
		{`foo\bar`, `provided search terms contain invalid character '\\'`},
		{"foo\x00bar", `provided search terms contain invalid character '\x00'`},
	}

	for _, tt := range tests {
		t.Run(tt.searchTerms, func(t *testing.T) {
			actual, err := parseSearchTerms(url.Values{queryParam: {tt.searchTerms}})

			assert.Empty(t, actual)
			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestParseSearchTermsAllowsAmpersandsAndApostrophes(t *testing.T) {
	tests := []string{
		"'s-Gravenhage",
		"Janssen & Fritsenplein",
	}

	for _, searchTerms := range tests {
		t.Run(searchTerms, func(t *testing.T) {
			actual, err := parseSearchTerms(url.Values{queryParam: {searchTerms}})

			require.NoError(t, err)
			assert.Equal(t, strings.ToLower(searchTerms), actual)
		})
	}
}

func TestParseExplain(t *testing.T) {
	tests := []struct {
		name      string
		values    url.Values
		want      bool
		wantError string
	}{
		{
			name:   "missing defaults to false",
			values: url.Values{},
			want:   false,
		},
		{
			name:   "explicit true",
			values: url.Values{explainParam: {"true"}},
			want:   true,
		},
		{
			name:   "explicit false",
			values: url.Values{explainParam: {"false"}},
			want:   false,
		},
		{
			name:      "uppercase is not accepted",
			values:    url.Values{explainParam: {"TRUE"}},
			wantError: "invalid value \"TRUE\" for query parameter 'explain': only 'true' or 'false' are allowed",
		},
		{
			name:      "numeric value is not accepted",
			values:    url.Values{explainParam: {"1"}},
			wantError: "invalid value \"1\" for query parameter 'explain': only 'true' or 'false' are allowed",
		},
		{
			name:      "arbitrary text is not accepted",
			values:    url.Values{explainParam: {"yes"}},
			wantError: "invalid value \"yes\" for query parameter 'explain': only 'true' or 'false' are allowed",
		},
		{
			name:      "empty value is not accepted",
			values:    url.Values{explainParam: {""}},
			wantError: "invalid value \"\" for query parameter 'explain': only 'true' or 'false' are allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := parseExplain(tt.values)

			if tt.wantError != "" {
				assert.False(t, actual)
				require.EqualError(t, err, tt.wantError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, actual)
			}
		})
	}
}

func TestParseQueryParamsAcceptsExplainAsKnownParam(t *testing.T) {
	query := url.Values{
		queryParam:           {"foo"},
		explainParam:         {"true"},
		"addresses[version]": {"1"},
	}

	_, _, _, _, _, _, _, explain, err := parseQueryParams(query)
	require.NoError(t, err)
	assert.True(t, explain)
}
