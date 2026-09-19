package tools

import (
	"strings"
	"testing"
)

func TestExpandQuerySubstitutesWholeWordsFromEverySet(t *testing.T) {
	sets := [][]string{{"db", "database"}, {"pg", "postgres", "yugabyte"}}

	variants := expandQuery("DB failover", sets)

	want := []string{"DB failover", "database failover"}
	if strings.Join(variants, "|") != strings.Join(want, "|") {
		t.Fatalf("variants = %v, want %v", variants, want)
	}
}

func TestExpandQueryLeavesAQueryWithoutSynonymsAlone(t *testing.T) {
	variants := expandQuery("archive rollout", [][]string{{"db", "database"}})

	if len(variants) != 1 || variants[0] != "archive rollout" {
		t.Fatalf("variants = %v, want the query alone", variants)
	}
}

func TestExpandQueryStopsAtTheVariantCap(t *testing.T) {
	terms := make([]string, 0, maxQueryVariants+5)
	for i := range maxQueryVariants + 5 {
		terms = append(terms, "term"+strings.Repeat("x", i))
	}

	variants := expandQuery("term", [][]string{terms})

	if len(variants) != maxQueryVariants {
		t.Fatalf("variants = %d, want the cap %d", len(variants), maxQueryVariants)
	}
}

func TestSynonymTermsDropsMultiWordAndEmptyTerms(t *testing.T) {
	terms := synonymTerms(" db , database,, foundation db ,fdb")

	if strings.Join(terms, "|") != "db|database|fdb" {
		t.Fatalf("terms = %v", terms)
	}
}
