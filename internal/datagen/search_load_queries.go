package datagen

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"unicode"
)

const (
	searchLoadMinimumWords = 3
	searchLoadMinimumTermLength = 3
	searchLoadSeedMask = 0x544C0AD
)

type searchLoadQueries struct {
	terms []string
	orders    [][]int
	workspace []string
	random    *rand.Rand
}

func newSearchLoadQueries(ctx context.Context, seed int64, workspace WorkspaceIdentity, planned int) (*searchLoadQueries, error) {
	rows, err := loadApprovedSearchCorpus(ctx, nil)
	if err != nil {
		return nil, err
	}
	corpusText := make([]string, 0, len(rows))
	for _, row := range rows {
		corpusText = append(corpusText, row.Text)
	}
	terms := searchLoadTerms(corpusText...)
	workspaceTerms := searchLoadTerms(workspace.Name, workspace.Slug)
	if len(terms) < 2 || len(workspaceTerms) == 0 {
		return nil, errors.New("qa datagen search-load: require at least two corpus terms and one workspace term")
	}
	random := rand.New(rand.NewSource(seed ^ searchLoadSeedMask))
	words := searchLoadMinimumWords
	for searchLoadCapacity(len(terms), words) < planned {
		words++
	}
	orders := make([][]int, 0, words)
	for range words {
		orders = append(orders, random.Perm(len(terms)))
	}
	return &searchLoadQueries{terms: terms, orders: orders, workspace: workspaceTerms, random: random}, nil
}

func searchLoadCapacity(termCount, words int) int {
	const maximum = int(^uint(0) >> 1)
	capacity := 1
	for range words {
		if capacity > maximum/termCount {
			return maximum
		}
		capacity *= termCount
	}
	return capacity
}

func searchLoadTerms(texts ...string) []string {
	seen := make(map[string]struct{})
	terms := make([]string, 0)
	for _, text := range texts {
		fields := strings.FieldsFunc(strings.ToLower(text), func(character rune) bool {
			return !unicode.IsLetter(character) && !unicode.IsDigit(character)
		})
		for _, field := range fields {
			if _, duplicate := seen[field]; duplicate || len(field) < searchLoadMinimumTermLength {
				continue
			}
			seen[field] = struct{}{}
			terms = append(terms, field)
		}
	}
	return terms
}

func (q *searchLoadQueries) text(index int) string {
	words := make([]string, 0, len(q.orders)+1)
	remainder := index
	for _, order := range q.orders {
		words = append(words, q.terms[order[remainder%len(q.terms)]])
		remainder /= len(q.terms)
	}
	words = append(words, q.workspace[q.random.Intn(len(q.workspace))])
	return strings.Join(words, " ")
}
