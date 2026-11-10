package datagen

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
)

//go:embed corpus.json
var corpusJSON []byte

type corpus struct {
	IssueTitles []string `json:"issue_titles"`
	Comments    []string `json:"comments"`
	Labels      []string `json:"labels"`
	Workflows   []string `json:"workflows"`
}

var curatedIssueTitles = []string{
	"Prevent duplicate webhook delivery",
	"Reconcile stale search documents",
	"Bound retry pressure during failover",
	"Preserve request identity across reconnects",
	"Repair missing relationship indexes",
	"Reduce project list latency",
	"Validate backup manifests before restore",
	"Expose audit lag by partition",
}

var curatedComments = []string{
	"I reproduced this with a clean workspace and captured the request trace.",
	"The fallback path needs the same verification as the primary path.",
	"The current behavior looks correct after the index catches up.",
	"Please keep the failure visible so the operator can distinguish an empty result.",
	"The recovery run completed and the stored digest matches the live image.",
}

var curatedLabels = []string{
	"reliability", "security", "performance", "data-integrity",
	"observability", "customer-impact", "migration", "developer-experience",
}

var curatedWorkflows = []string{
	"Ready for review", "Waiting on dependency", "Needs reproduction", "Release verification",
}

// Content produces deterministic text and timestamps from one seed.
type Content struct {
	random        *rand.Rand
	faker         *gofakeit.Faker
	corpus        corpus
	referenceTime time.Time
}

// NewContent loads the embedded corpus and seeds every content source.
func NewContent(ctx context.Context, seed int64) (*Content, error) {
	var cached corpus
	if err := json.Unmarshal(corpusJSON, &cached); err != nil {
		return nil, loggedError(ctx, "qa datagen: decode embedded corpus", err)
	}
	randomSource := rand.New(rand.NewSource(seed))
	fakerSeed := randomSource.Uint64()
	if fakerSeed == 0 {
		fakerSeed = 1
	}
	return &Content{
		random:        randomSource,
		faker:         gofakeit.New(fakerSeed),
		corpus:        cached,
		referenceTime: seededReferenceTime(seed),
	}, nil
}

// IssueTitle returns a stable realistic issue title.
func (c *Content) IssueTitle(index int) string {
	values := append(append([]string{}, curatedIssueTitles...), c.corpus.IssueTitles...)
	return fmt.Sprintf("%s [%03d]", c.pick(values), index+1)
}

// Comment returns a stable engineering comment.
func (c *Content) Comment() string {
	values := append(append([]string{}, curatedComments...), c.corpus.Comments...)
	if c.random.Intn(3) == 0 {
		return c.faker.Sentence(14)
	}
	return c.pick(values)
}

// Name returns a deterministic human name.
func (c *Content) Name() string {
	return c.faker.Name()
}

// Sentence returns deterministic sentence text.
func (c *Content) Sentence() string {
	return c.faker.Sentence(14)
}

// Label returns a stable label name.
func (c *Content) Label(index int) string {
	values := append(append([]string{}, curatedLabels...), c.corpus.Labels...)
	return fmt.Sprintf("%s-%02d", slugify(c.pick(values)), index+1)
}

// Workflow returns a stable custom workflow state.
func (c *Content) Workflow(index int) string {
	values := append(append([]string{}, curatedWorkflows...), c.corpus.Workflows...)
	return fmt.Sprintf("%s %d", c.pick(values), index+1)
}

// Paragraph returns deterministic descriptive text.
func (c *Content) Paragraph() string {
	return c.faker.Paragraph(1, 3, 12, " ")
}

// Timestamp returns a deterministic timestamp inside the supplied range.
func (c *Content) Timestamp(start, end time.Time) time.Time {
	return c.faker.DateRange(start, end).UTC()
}

// ReferenceTime returns the stable present used for past and future values.
func (c *Content) ReferenceTime() time.Time {
	return c.referenceTime
}

func (c *Content) pick(values []string) string {
	return values[c.random.Intn(len(values))]
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		isLetter := character >= 'a' && character <= 'z'
		isNumber := character >= '0' && character <= '9'
		if isLetter || isNumber {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func seededReferenceTime(seed int64) time.Time {
	offset := seed % 3650
	if offset < 0 {
		offset = -offset
	}
	return time.Date(2025, time.January, 1, 12, 0, 0, 0, time.UTC).
		Add(time.Duration(offset) * 24 * time.Hour)
}
