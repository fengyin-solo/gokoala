package query_expansion

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	fdomain "github.com/PDOK/gokoala/internal/ogc/features/domain"
	"github.com/PDOK/gokoala/internal/ogc/features_search/domain"
)

const regexPrefix = "regex:"

const (
	// Limits on the number of entries exposed in the search explanation. These caps
	// make sure the (potentially large) internal candidate set is never fully exposed.
	maxExplainedRewrites        = 100
	maxExplainedWords           = 100
	maxExplainedSynonymsPerWord = 50
)

// QueryExpansion query expansion involves evaluating a user's input (what words were typed into the search query area)
// and expanding the search query to match additional results, see https://en.wikipedia.org/wiki/Query_expansion
type QueryExpansion struct {
	rewrites    []rewrite
	synonyms    map[string][]string
	maxSynonyms int
}

func NewQueryExpansion(rewritesFile, synonymsFile string, maxSynonyms int) (*QueryExpansion, error) {
	rewrites, rewErr := readRewrites(rewritesFile)
	synonyms, synErr := readSynonyms(synonymsFile)

	return &QueryExpansion{
		rewrites:    rewrites,
		synonyms:    synonyms,
		maxSynonyms: maxSynonyms,
	}, errors.Join(rewErr, synErr)
}

// Expand Perform query expansion, see https://en.wikipedia.org/wiki/Query_expansion.
// Besides the search query used against the datasource, a deterministic explanation
// of the applied rewrites and synonym expansions is returned. The explanation never
// contains SQL or other datasource internals.
func (s QueryExpansion) Expand(ctx context.Context, searchTerms string) (*domain.SearchQuery, *fdomain.Expansion, error) {
	expandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	rewritten, rewriteSteps, err := performRewrites(expandCtx, strings.ToLower(searchTerms), s.rewrites)
	if err != nil {
		return nil, nil, err
	}
	words, wordsWithoutSynonyms, wordsWithSynonyms, truncatedWords, err :=
		expandSynonyms(expandCtx, rewritten, s.synonyms, s.maxSynonyms)
	if err != nil {
		return nil, nil, err
	}
	searchQuery := domain.NewSearchQuery(words, wordsWithoutSynonyms, wordsWithSynonyms)
	explanation := newExpansion(searchTerms, rewritten, rewriteSteps, words,
		wordsWithoutSynonyms, wordsWithSynonyms, truncatedWords)
	return searchQuery, explanation, expandCtx.Err()
}

func performRewrites(ctx context.Context, input string, rewrites []rewrite) (string, []fdomain.RewriteStep, error) {
	var steps []fdomain.RewriteStep
	for _, r := range rewrites {
		before := input
		if r.regex != nil {
			input = r.regex.ReplaceAllString(input, r.original)
		} else {
			input = strings.ReplaceAll(input, r.alternative, r.original)
		}
		// only report rewrites that actually changed the input
		if input != before {
			steps = append(steps, fdomain.RewriteStep{
				From: strings.TrimPrefix(r.alternative, regexPrefix),
				To:   r.original,
			})
		}
	}
	return input, steps, ctx.Err()
}

func expandSynonyms(ctx context.Context, input string, mapping map[string][]string,
	maxSynonyms int) ([]string, map[string]struct{}, map[string][]string, map[string]struct{}, error) {

	words := uniqueSlice(strings.Fields(input))

	wordsWithSynonyms := make(map[string][]string)
	truncatedWords := make(map[string]struct{})
	for _, word := range words {
		variants := []string{word}
		maxSynonymsReached := false

		for i := 0; i < len(variants) && !maxSynonymsReached; i++ {
			existingVariant := variants[i]
			positions := mapPositions(existingVariant, mapping)

			// sort by longest length, when equal by smallest start position
			sort.Slice(positions, func(i, j int) bool {
				if positions[i].length != positions[j].length {
					return positions[i].length > positions[j].length
				}
				return positions[i].start < positions[j].start
			})

			for _, newVariant := range generateNewVariants(existingVariant, positions) {
				if err := ctx.Err(); err != nil {
					return nil, nil, nil, nil, err // timeout encountered
				}
				if !slices.Contains(variants, newVariant) {
					if len(variants) > maxSynonyms {
						log.Printf("max synonyms (%d) exceeded, skipping "+
							"further expansion for word: %s", maxSynonyms, word)
						maxSynonymsReached = true
						truncatedWords[word] = struct{}{}
						break
					}
					variants = append(variants, newVariant) // continue for-loop by appending to slice
					wordsWithSynonyms[word] = append(wordsWithSynonyms[word], newVariant)
				}
			}
		}
	}

	wordsWithoutSynonyms := make(map[string]struct{})
	for _, word := range words {
		if _, ok := wordsWithSynonyms[word]; ok {
			continue
		}
		wordsWithoutSynonyms[word] = struct{}{}
	}
	return words, wordsWithoutSynonyms, wordsWithSynonyms, truncatedWords, ctx.Err()
}

// newExpansion builds a deterministic, capped explanation of the rewrites and
// synonym expansions that were applied.
func newExpansion(originalInput, normalizedInput string, rewriteSteps []fdomain.RewriteStep,
	words []string, wordsWithoutSynonyms map[string]struct{}, wordsWithSynonyms map[string][]string,
	truncatedWords map[string]struct{}) *fdomain.Expansion {

	result := &fdomain.Expansion{
		OriginalInput:   originalInput,
		NormalizedInput: normalizedInput,
		Words:           capStrings(words, maxExplainedWords),
		Rewrites:        capRewriteSteps(rewriteSteps, maxExplainedRewrites),
	}
	// make sure the required words field is always serialized as an array, never null
	if result.Words == nil {
		result.Words = []string{}
	}

	// synonyms per word, in the deterministic order of the (capped) input words
	for _, word := range result.Words {
		synonyms, ok := wordsWithSynonyms[word]
		if !ok {
			continue
		}
		entry := fdomain.WordSynonyms{Word: word, Synonyms: uniqueSorted(synonyms)}
		if len(entry.Synonyms) > maxExplainedSynonymsPerWord {
			entry.Synonyms = entry.Synonyms[:maxExplainedSynonymsPerWord]
			entry.Truncated = true
		}
		if _, isTruncated := truncatedWords[word]; isTruncated {
			entry.Truncated = true
		}
		result.Synonyms = append(result.Synonyms, entry)
	}

	// words without synonyms and truncated words, in word order
	for _, word := range result.Words {
		if _, ok := wordsWithoutSynonyms[word]; ok {
			result.WordsWithoutSynonyms = append(result.WordsWithoutSynonyms, word)
		}
		if _, ok := truncatedWords[word]; ok {
			result.TruncatedWords = append(result.TruncatedWords, word)
		}
	}

	return result
}

// uniqueSorted returns a sorted, deduplicated copy so explanation output is deterministic.
func uniqueSorted(s []string) []string {
	result := slices.Clone(s)
	slices.Sort(result)
	return slices.Compact(result)
}

func capStrings(s []string, max int) []string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

func capRewriteSteps(s []fdomain.RewriteStep, max int) []fdomain.RewriteStep {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// replaces all duplicates in a slice (note: slices.compact() only replaces consecutive duplicates).
func uniqueSlice(s []string) []string {
	var results []string
	seen := make(map[string]bool)
	for _, entry := range s {
		if !seen[entry] {
			seen[entry] = true
			results = append(results, entry)
		}
	}
	return results
}

// position is a substring match in the given search term
type position struct {
	start       int
	length      int
	alternative string
}

func (p position) end() int {
	return p.start + p.length
}

func (p position) replace(input string) string {
	return input[:p.start] + p.alternative + input[p.end():]
}

func mapPositions(input string, mapping map[string][]string) []position {
	var results []position

	for original, alternatives := range mapping {
		for i := 0; i < len(input); {
			originalPos := strings.Index(input[i:], original)
			if originalPos == -1 {
				break
			}

			for _, alternative := range alternatives {
				results = append(results, position{
					start:       i + originalPos,
					length:      len(original),
					alternative: alternative,
				})
			}
			i += originalPos + 1
		}
	}
	return results
}

func generateNewVariants(input string, positions []position) []string {
	var results []string
	for _, pos := range positions {
		if !hasOverlap(pos, positions) {
			results = append(results, pos.replace(input))
		}
	}
	return results
}

// We need to check for overlapping synonyms for situations like:
//
// synonyms = goeverneur,goev,gouverneur,gouv
// input = 1e gouverneurstraat
// synonyms key (original) => gouv
// synonyms value (alternative) = goeverneur
// resulting string = 1e goeverneurERNEURstraat <-- not what we want
func hasOverlap(current position, all []position) bool {
	for _, other := range all {
		if other.length <= current.length {
			continue
		}
		if current.start < other.end() && other.start < current.end() {
			return true
		}
	}
	return false
}

type rewrite struct {
	original    string
	alternative string
	regex       *regexp.Regexp
}

func readRewrites(filepath string) ([]rewrite, error) {
	records, err := readCsvFile(filepath)
	if err != nil {
		return nil, err
	}

	var rewrites []rewrite
	for _, row := range records {
		original := strings.ToLower(row[0])
		for i := 1; i < len(row); i++ {
			alternative := strings.ToLower(row[i])
			r := rewrite{
				original:    original,
				alternative: alternative,
			}
			if strings.HasPrefix(alternative, regexPrefix) {
				regexPattern := alternative[len(regexPrefix):]
				regexPattern = regexPattern[1 : len(regexPattern)-1]
				r.regex, err = regexp.Compile(regexPattern)
				if err != nil {
					return nil, fmt.Errorf("failed to compile regex %s from rewrites file: %w", regexPattern, err)
				}
			}
			rewrites = append(rewrites, r)
		}
	}
	return rewrites, nil
}

func readSynonyms(filepath string) (map[string][]string, error) {
	records, err := readCsvFile(filepath)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]string)
	for _, row := range records {
		key := strings.ToLower(row[0])

		// add all alternatives
		result[key] = make([]string, 0)
		for i := 1; i < len(row); i++ {
			result[key] = append(result[key], strings.ToLower(row[i]))
		}

		// make result map bidirectional, so:
		// 1e => one,first | 2e => second
		// becomes:
		// 1e => one,first | 2e => second | one => 1e,first | first => 1e,one | second => 2e
		for _, alt := range result[key] {
			if _, ok := result[alt]; !ok {
				result[alt] = make([]string, 0)
			}
			result[alt] = append(result[alt], key)
			for _, other := range result[key] {
				if other != alt { // skip self
					result[alt] = append(result[alt], other)
				}
			}
		}
	}

	// avoid too short synonyms to prevent to many invalid synonym/combinations
	for k, v := range result {
		if err = assertSynonymLength(k); err != nil {
			return nil, err
		}
		for _, variant := range v {
			if err = assertSynonymLength(variant); err != nil {
				return nil, err
			}
		}
	}

	return result, nil
}

func readCsvFile(filepath string) ([][]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1 // allow variable number of columns per row, also allow blank lines
	reader.Comment = '#'        // allow comments in CSV

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSV file: %w", err)
	}
	return records, nil
}

func assertSynonymLength(syn string) error {
	if len(syn) < 2 {
		return fmt.Errorf("failed to parse CSV file: synonym '%s' is too short, should be at least 2 chars long", syn)
	}
	return nil
}
