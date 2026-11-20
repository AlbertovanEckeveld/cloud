package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/knowledge"
)

// Tools the model may call (ADR 0024). They only read, as the user: every
// item they return passed its source's permission check, and reading or
// listing works on numbers the model was given in this answer, never on
// IDs it could make up.
const (
	maxToolRounds  = 4     // rounds of tool calls per question
	maxSources     = 20    // items numbered in one answer
	toolResults    = 8     // items one search returns
	readChars      = 8000  // characters one read returns
	readBudget     = 40000 // characters all reads of one answer return
	maxSearchQuery = 200
)

var tools = []tool{
	{Type: "function", Function: toolFunction{
		Name:        "zoeken",
		Description: "Zoek in alles wat de gebruiker mag zien (bestanden, en later notities, agenda, foto's, mail) op naam, woorden en betekenis. Geeft genummerde items terug.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"zoekopdracht": map[string]any{"type": "string", "description": "Waar je naar zoekt, in een paar woorden."},
		}, "required": []string{"zoekopdracht"}},
	}},
	{Type: "function", Function: toolFunction{
		Name:        "lezen",
		Description: "Lees de tekst van een item met zijn bronnummer, bijvoorbeeld om een document samen te vatten of iets op te zoeken.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"nummer": map[string]any{"type": "integer", "description": "Het bronnummer, zoals [3]."},
		}, "required": []string{"nummer"}},
	}},
	{Type: "function", Function: toolFunction{
		Name:        "inhoud",
		Description: "Toon wat er in een map (of ander verzamelitem) zit, met zijn bronnummer. De inhoud krijgt eigen nummers.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"nummer": map[string]any{"type": "integer", "description": "Het bronnummer van de map."},
		}, "required": []string{"nummer"}},
	}},
}

// Stream is how an answer reaches the user while it is made.
type Stream struct {
	Delta func(text string) // a piece of the answer
	Step  func(text string) // something the assistant did, such as a search
	Reset func()            // forget the text so far: the model went on with tools
}

// turn is the state of one answer: the numbered items the model may refer
// to, and how much it has read.
type turn struct {
	s       *Service
	user    uuid.UUID
	sources []Source
	byKey   map[key]int
	read    int
	steps   []string
	stream  Stream
}

func newTurn(s *Service, user uuid.UUID, passages []passage, stream Stream) *turn {
	t := &turn{s: s, user: user, byKey: map[key]int{}, stream: stream}
	for _, p := range passages {
		t.number(p.source.Item)
	}
	return t
}

// number gives an item its source number (once), within maxSources.
func (t *turn) number(it knowledge.Item) (int, bool) {
	k := key{it.Source, it.ID}
	if n, ok := t.byKey[k]; ok {
		return n, true
	}
	if len(t.sources) >= maxSources {
		return 0, false
	}
	n := len(t.sources) + 1
	t.byKey[k] = n
	t.sources = append(t.sources, Source{N: n, Item: it})
	return n, true
}

func (t *turn) step(text string) {
	t.steps = append(t.steps, text)
	if t.stream.Step != nil {
		t.stream.Step(text)
	}
}

// run carries out one tool call and returns its result for the model.
func (t *turn) run(ctx context.Context, call toolCall) (string, error) {
	var args struct {
		Query  string  `json:"zoekopdracht"`
		Number float64 `json:"nummer"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "Ongeldige argumenten.", nil
	}
	switch call.Function.Name {
	case "zoeken":
		return t.search(ctx, strings.TrimSpace(args.Query))
	case "lezen":
		return t.readItem(ctx, int(args.Number))
	case "inhoud":
		return t.list(ctx, int(args.Number))
	}
	return "Onbekend hulpmiddel.", nil
}

func (t *turn) search(ctx context.Context, query string) (string, error) {
	if n := utf8.RuneCountInString(query); n == 0 || n > maxSearchQuery {
		return "Geef een zoekopdracht van 1 tot 200 tekens.", nil
	}
	t.step("Zocht naar “" + query + "”")
	items, err := t.s.Search(ctx, t.user, query, nil)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("<resultaat>\n")
	shown := 0
	for _, it := range items {
		if shown == toolResults {
			break
		}
		n, ok := t.number(it)
		if !ok {
			break
		}
		shown++
		fmt.Fprintf(&b, "[%d] %s (%s) — %s", n, attr(it.Title), it.Kind, attr(it.Location))
		if it.Snippet != "" {
			b.WriteString(": " + neutralise(strings.NewReplacer("\x02", "", "\x03", "").Replace(it.Snippet)))
		}
		b.WriteByte('\n')
	}
	if shown == 0 {
		b.WriteString("Niets gevonden.\n")
	}
	b.WriteString("</resultaat>")
	return b.String(), nil
}

// item returns numbered item n if the user may still see it.
func (t *turn) item(ctx context.Context, n int) (knowledge.Item, bool, error) {
	if n < 1 || n > len(t.sources) {
		return knowledge.Item{}, false, nil
	}
	src := t.sources[n-1]
	allowed, err := t.s.visible(ctx, t.user, []key{{src.Source, src.ID}})
	if err != nil {
		return knowledge.Item{}, false, err
	}
	it, ok := allowed[key{src.Source, src.ID}]
	return it, ok, nil
}

func (t *turn) readItem(ctx context.Context, n int) (string, error) {
	it, ok, err := t.item(ctx, n)
	if err != nil {
		return "", err
	}
	if !ok {
		return "Onbekend of niet beschikbaar bronnummer. Gebruik een nummer uit de bronnen of een zoekresultaat.", nil
	}
	if t.read >= readBudget {
		return "Je hebt voor deze vraag genoeg gelezen; antwoord met wat je hebt.", nil
	}
	t.step("Las “" + it.Title + "”")
	_, body, err := t.s.byName[it.Source].Text(ctx, it.ID)
	if errors.Is(err, knowledge.ErrNotFound) {
		return "Dit item heeft geen tekst.", nil
	}
	if err != nil {
		return "", err
	}
	text := body[:advance(body, 0, min(readChars, readBudget-t.read))]
	t.read += utf8.RuneCountInString(text)
	return fmt.Sprintf("<bron nr=\"%d\" titel=\"%s\" locatie=\"%s\">\n%s\n</bron>", n, attr(it.Title), attr(it.Location), neutralise(text)), nil
}

func (t *turn) list(ctx context.Context, n int) (string, error) {
	it, ok, err := t.item(ctx, n)
	if err != nil {
		return "", err
	}
	c, isContainer := t.s.byName[it.Source].(knowledge.Container)
	if !ok || !isContainer {
		return "Onbekend bronnummer, of dit item bevat niets.", nil
	}
	t.step("Bekeek de inhoud van “" + it.Title + "”")
	children, err := c.Children(ctx, t.user, it.ID)
	if errors.Is(err, knowledge.ErrNotFound) {
		return "Dit item bevat niets.", nil
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("<resultaat>\n")
	for _, ch := range children {
		m, ok := t.number(ch)
		if !ok {
			b.WriteString("… en meer (niet genummerd)\n")
			break
		}
		fmt.Fprintf(&b, "[%d] %s (%s)\n", m, attr(ch.Title), ch.Kind)
	}
	if len(children) == 0 {
		b.WriteString("Leeg.\n")
	}
	b.WriteString("</resultaat>")
	return b.String(), nil
}
