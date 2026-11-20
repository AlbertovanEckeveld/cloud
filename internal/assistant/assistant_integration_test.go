//go:build integration

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/internal/core/knowledge"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

// fakeSource stands in for a module: items with text, and who may see them.
type fakeSource struct {
	name    string
	mu      sync.Mutex
	files   map[uuid.UUID]*fakeFile
	folders map[uuid.UUID][]uuid.UUID // folder -> files in it (a knowledge.Container)
}

type fakeFile struct {
	name    string
	version uuid.UUID
	readAt  time.Time
	text    string
	viewers []uuid.UUID
}

func newSource(name string) *fakeSource {
	return &fakeSource{name: name, files: map[uuid.UUID]*fakeFile{}, folders: map[uuid.UUID][]uuid.UUID{}}
}

// Children implements knowledge.Container for folders whose files the user
// may all see.
func (f *fakeSource) Children(_ context.Context, user uuid.UUID, id string) ([]knowledge.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kids, ok := f.folders[uuid.MustParse(id)]
	if !ok {
		return nil, knowledge.ErrNotFound
	}
	var out []knowledge.Item
	for _, k := range kids {
		if file := f.files[k]; file != nil && slices.Contains(file.viewers, user) {
			out = append(out, f.item(k, file))
		}
	}
	return out, nil
}

func (f *fakeSource) Name() string { return f.name }

func stampOf(file *fakeFile) string {
	return file.version.String() + "@" + file.readAt.Format(time.RFC3339Nano)
}

func (f *fakeSource) Index(_ context.Context, after string, limit int) ([]knowledge.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []knowledge.Ref
	for id, file := range f.files {
		if id.String() > after {
			out = append(out, knowledge.Ref{ID: id.String(), Stamp: stampOf(file)})
		}
	}
	slices.SortFunc(out, func(a, b knowledge.Ref) int { return strings.Compare(a.ID, b.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeSource) Count(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.files), nil
}

func (f *fakeSource) Text(_ context.Context, id string) (knowledge.Ref, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[uuid.MustParse(id)]
	if !ok {
		return knowledge.Ref{}, "", knowledge.ErrNotFound
	}
	return knowledge.Ref{ID: id, Stamp: stampOf(file)}, file.text, nil
}

func (f *fakeSource) item(id uuid.UUID, file *fakeFile) knowledge.Item {
	return knowledge.Item{Source: f.name, ID: id.String(), Kind: "file", Title: file.name, Location: "Mijn bestanden", Link: "/files"}
}

func (f *fakeSource) Visible(_ context.Context, user uuid.UUID, ids []string) ([]knowledge.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []knowledge.Item{}
	for _, id := range ids {
		nid := uuid.MustParse(id)
		if file, ok := f.files[nid]; ok && slices.Contains(file.viewers, user) {
			out = append(out, f.item(nid, file))
		} else if _, ok := f.folders[nid]; ok {
			out = append(out, knowledge.Item{Source: f.name, ID: id, Kind: "folder", Title: "Projecten", Link: "/files/" + id})
		}
	}
	return out, nil
}

func (f *fakeSource) Search(_ context.Context, user uuid.UUID, q string) ([]knowledge.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []knowledge.Item{}
	for id, file := range f.files {
		for _, w := range strings.Split(strings.ToLower(q), " or ") {
			if slices.Contains(file.viewers, user) && strings.Contains(strings.ToLower(file.name), strings.TrimSpace(w)) {
				out = append(out, f.item(id, file))
				break
			}
		}
	}
	return out, nil
}

// embed turns text into a vector of hashed word counts: texts sharing words
// are close, which is enough to test search by meaning.
func embed(text string) []float32 {
	v := make([]float32, 64)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New32a()
		h.Write([]byte(strings.Trim(w, ".,?!")))
		v[h.Sum32()%64]++
	}
	var n float64
	for _, x := range v {
		n += float64(x * x)
	}
	if n > 0 {
		for i := range v {
			v[i] /= float32(math.Sqrt(n))
		}
	}
	return v
}

// provider is a fake OpenAI-compatible server that records what the chat
// model was given.
type provider struct {
	srv         *httptest.Server
	mu          sync.Mutex
	prompts     []string
	embedded    int
	refuseTools bool // answer 400 to requests with tools
}

func newProvider(t *testing.T) *provider {
	p := &provider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"chat-m"},{"id":"embed-m"},{"id":"embed-2"}]}`)
		case "/v1/embeddings":
			var in struct {
				Model string
				Input []string
			}
			_ = json.Unmarshal(body, &in)
			if in.Model == "chat-m" {
				http.Error(w, `{"error":{"message":"models/chat-m is not supported for embedContent"}}`, http.StatusNotFound)
				return
			}
			type item struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}
			var out struct {
				Data []item `json:"data"`
			}
			for i, s := range in.Input {
				out.Data = append(out.Data, item{i, embed(s)})
			}
			p.mu.Lock()
			p.embedded += len(in.Input)
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		case "/v1/chat/completions":
			p.mu.Lock()
			p.prompts = append(p.prompts, string(body))
			refuse := p.refuseTools
			p.mu.Unlock()
			var in struct {
				Messages []struct {
					Role, Content string
					ToolCalls     []struct {
						ExtraContent struct {
							Google struct {
								ThoughtSignature string `json:"thought_signature"`
							} `json:"google"`
						} `json:"extra_content"`
					} `json:"tool_calls"`
				}
				Tools []any
			}
			_ = json.Unmarshal(body, &in)
			if refuse && len(in.Tools) > 0 {
				http.Error(w, `{"error":{"message":"tools are not supported"}}`, http.StatusBadRequest)
				return
			}
			// Like Gemini: a tool call must come back with the signature it was sent with.
			for _, m := range in.Messages {
				for _, c := range m.ToolCalls {
					if c.ExtraContent.Google.ThoughtSignature != "sig-1" {
						http.Error(w, `{"error":{"message":"Function call is missing a thought_signature"}}`, http.StatusBadRequest)
						return
					}
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			call := func(name, args string) {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"extra_content\":{\"google\":{\"thought_signature\":\"sig-1\"}},\"function\":{\"name\":%q,\"arguments\":\"\"}}]}}]}\n\n", name)
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":%q}}]}}]}\n\n", args)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}
			last := in.Messages[len(in.Messages)-1]
			first := ""
			for _, m := range in.Messages {
				if m.Role == "user" {
					first = m.Content
				}
			}
			if len(in.Tools) > 0 {
				switch {
				case last.Role == "user" && strings.Contains(last.Content, "ZOEKTEST"):
					call("zoeken", `{"zoekopdracht":"dakkapel"}`)
					return
				case last.Role == "user" && strings.Contains(last.Content, "ONGELDIG"):
					call("lezen", `{"nummer":99}`)
					return
				case last.Role == "tool" && strings.Contains(last.Content, "<resultaat>") && strings.Contains(first, "ZOEKTEST"):
					if m := regexp.MustCompile(`\[(\d+)\]`).FindStringSubmatch(last.Content); m != nil {
						call("lezen", `{"nummer":`+m[1]+`}`)
						return
					}
				}
			}
			for _, part := range []string{"Volgens ", "[1] kost ", "het 5000 euro."} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", part)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

type env struct {
	svc   *Service
	pool  *pgxpool.Pool
	files *fakeSource
	notes *fakeSource
	prov  *provider
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := database.Open(ctx, config.Database{URL: config.Secret(os.Getenv("DATABASE_URL"))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fsys, err := migrations.For("assistant")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool, logger, database.Module{Name: "assistant", FS: fsys}); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	sl, _ := sealer.New(key)
	files, notes := newSource("drive"), newSource("notes")
	e := &env{svc: New(pool, sl, []knowledge.Source{files, notes}, Options{Logger: logger}), pool: pool, files: files, notes: notes, prov: newProvider(t)}

	// The settings row is shared: put it back afterwards.
	before, err := e.svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `UPDATE assistant.settings SET chat_provider = $1, chat_model = NULLIF($2, ''),
			embed_provider = $3, embed_model = NULLIF($4, '')`, before.ChatProvider, before.ChatModel, before.EmbedProvider, before.EmbedModel)
		for _, src := range []*fakeSource{files, notes} {
			for id := range src.files {
				_, _ = pool.Exec(ctx, `DELETE FROM assistant.chunks WHERE item_id = $1`, id.String())
				_, _ = pool.Exec(ctx, `DELETE FROM assistant.documents WHERE item_id = $1`, id.String())
			}
		}
		_, _ = pool.Exec(ctx, `DELETE FROM assistant.providers WHERE name LIKE 'it-%'`)
	})
	return e
}

func (e *env) user(t *testing.T) uuid.UUID {
	t.Helper()
	id := newID()
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM assistant.conversations WHERE user_id = $1`, id)
	})
	return id
}

func (e *env) configure(t *testing.T, embedModel string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	p, err := e.svc.SaveProvider(ctx, newID(), nil, ProviderInput{Name: fmt.Sprintf("it-%d", time.Now().UnixNano()), BaseURL: e.prov.srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.External || p.HasKey {
		t.Fatalf("local provider: %+v", p)
	}
	if err := e.svc.SaveSettings(ctx, Settings{ChatProvider: &p.ID, ChatModel: "chat-m", EmbedProvider: &p.ID, EmbedModel: embedModel}); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestAssistant(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ann, bob := e.user(t), e.user(t)
	quote, salary, shared := newID(), newID(), newID()
	e.files.files[quote] = &fakeFile{name: "offerte.pdf", version: newID(), viewers: []uuid.UUID{ann},
		text: "Offerte voor de dakkapel. De dakkapel kost 5000 euro inclusief plaatsing."}
	e.files.files[salary] = &fakeFile{name: "salaris.txt", version: newID(), viewers: []uuid.UUID{bob},
		text: "Geheim: het salaris van Bob en de kosten van de dakkapel bij de buren."}
	e.files.files[shared] = &fakeFile{name: "notulen.md", version: newID(), viewers: []uuid.UUID{ann, bob},
		text: "Notulen van de vergadering over de tuin en het hek."}

	note := newID()
	e.notes.files[note] = &fakeFile{name: "notitie dakkapel", version: newID(), viewers: []uuid.UUID{ann},
		text: "Notitie: de aannemer belt volgende week over de planning van de dakkapel."}

	pid := e.configure(t, "embed-m")
	if models, err := e.svc.TestProvider(ctx, pid); err != nil || len(models) != 3 {
		t.Fatalf("test provider: %v %v", models, err)
	}
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.Status(ctx); !st.Configured || st.Files != 4 || st.Indexed != 4 || st.Failed != 0 {
		t.Fatalf("status: %+v", st)
	}

	// Search by meaning: only what the user may see.
	results, err := e.svc.Search(ctx, ann, "wat kost de dakkapel", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].ID != quote.String() || slices.ContainsFunc(results, func(r knowledge.Item) bool { return r.ID == salary.String() }) {
		t.Fatalf("search for ann: %+v", results)
	}

	// Items of every source are found, each with its source.
	if !slices.ContainsFunc(results, func(r knowledge.Item) bool { return r.Source == "notes" && r.ID == note.String() }) {
		t.Errorf("the note is not found: %+v", results)
	}
	if bobs, _ := e.svc.Search(ctx, bob, "dakkapel planning", nil); slices.ContainsFunc(bobs, func(r knowledge.Item) bool { return r.Source == "notes" }) {
		t.Errorf("bob finds ann's note: %+v", bobs)
	}
	if only, _ := e.svc.Search(ctx, ann, "dakkapel", []string{"drive"}); slices.ContainsFunc(only, func(r knowledge.Item) bool { return r.Source != "drive" }) {
		t.Errorf("search limited to drive returns other sources: %+v", only)
	}

	// Chat: the model only reads what the user may see.
	conv, err := e.svc.CreateConversation(ctx, ann)
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	msg, err := e.svc.Ask(ctx, ann, conv.ID, "Wat kost de dakkapel?", nil, Stream{Delta: func(s string) { streamed.WriteString(s) }})
	if err != nil {
		t.Fatal(err)
	}
	prompt := e.prov.prompts[len(e.prov.prompts)-1]
	if !strings.Contains(prompt, "5000 euro inclusief plaatsing") || strings.Contains(prompt, "salaris") {
		t.Fatalf("prompt for ann:\n%s", prompt)
	}
	if streamed.String() != msg.Content || msg.Content != "Volgens [1] kost het 5000 euro." ||
		len(msg.Sources) == 0 || msg.Sources[0].ID != quote.String() || msg.Sources[0].N != 1 || msg.Sources[0].Source != "drive" {
		t.Fatalf("answer: %+v", msg)
	}
	got, err := e.svc.GetConversation(ctx, ann, conv.ID)
	if err != nil || len(got.Messages) != 2 || got.Title != "Wat kost de dakkapel?" {
		t.Fatalf("conversation: %+v %v", got, err)
	}
	// Nobody else reads it.
	if _, err := e.svc.GetConversation(ctx, bob, conv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob reads ann's conversation: %v", err)
	}
	if _, err := e.svc.Ask(ctx, bob, conv.ID, "?", nil, Stream{Delta: func(string) {}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob asks in ann's conversation: %v", err)
	}
	// Losing access hides the source later too.
	e.files.files[quote].viewers = nil
	got, _ = e.svc.GetConversation(ctx, ann, conv.ID)
	if slices.ContainsFunc(got.Messages[1].Sources, func(s Source) bool { return s.ID == quote.String() }) {
		t.Errorf("a source ann can no longer open is still listed: %+v", got.Messages[1].Sources)
	}

	// A question about one item: its whole text goes along, but only for
	// someone who may see it.
	conv2, _ := e.svc.CreateConversation(ctx, ann)
	about := []Focus{{Source: "notes", ID: note.String()}}
	if _, err := e.svc.Ask(ctx, ann, conv2.ID, "Vat samen", about, Stream{Delta: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if p := e.prov.prompts[len(e.prov.prompts)-1]; !strings.Contains(p, "aannemer belt volgende week") {
		t.Errorf("the focused note is not in ann's prompt:\n%s", p)
	}
	// Only that item: words in the question do not pull in other files.
	if _, err := e.svc.Ask(ctx, ann, conv2.ID, "Vat dit samen en vergelijk het niet met de notulen", about, Stream{Delta: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if p := e.prov.prompts[len(e.prov.prompts)-1]; strings.Contains(p, `nr=\"2\"`) || !strings.Contains(p, "De vraag gaat over [1]") {
		t.Errorf("a question about one item brought in others:\n%s", p)
	}
	bobConv, _ := e.svc.CreateConversation(ctx, bob)
	if _, err := e.svc.Ask(ctx, bob, bobConv.ID, "Vat samen", about, Stream{Delta: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if p := e.prov.prompts[len(e.prov.prompts)-1]; strings.Contains(p, "aannemer") {
		t.Errorf("ann's note reached bob's prompt through focus:\n%s", p)
	}
	// A folder: what it holds.
	folder := newID()
	e.files.folders[folder] = []uuid.UUID{quote, shared}
	if _, err := e.svc.Ask(ctx, ann, conv2.ID, "Wat staat hier?", []Focus{{Source: "drive", ID: folder.String()}}, Stream{Delta: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	// Ann lost access to the quote above: the folder shows only what she may see.
	if p := e.prov.prompts[len(e.prov.prompts)-1]; !strings.Contains(p, "Inhoud (1 item)") || !strings.Contains(p, "- notulen.md") || strings.Contains(p, "- offerte.pdf") {
		t.Errorf("the folder's contents in the prompt:\n%s", p)
	}
	var input *InputError
	if _, err := e.svc.Ask(ctx, ann, conv2.ID, "?", []Focus{{Source: "elders", ID: "x"}}, Stream{Delta: func(string) {}}); !errors.As(err, &input) {
		t.Errorf("focus on an unknown source: %v", err)
	}

	// A changed file is indexed again; a removed one is forgotten.
	e.files.files[shared].version, e.files.files[shared].text = newID(), "Notulen: het hek wordt groen geverfd."
	delete(e.files.files, salary)
	before := e.prov.embedded
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if e.prov.embedded-before != 1 {
		t.Errorf("re-embedded %d chunks, want only the changed file's 1", e.prov.embedded-before)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM assistant.chunks WHERE item_id = $1`, salary.String()).Scan(&n)
	if n != 0 {
		t.Errorf("a removed file still has %d chunks", n)
	}

	// The same version read again (new file types): indexed again too.
	e.files.files[quote].text = "Offerte voor de dakkapel: 5000 euro. Bijlage met tekeningen."
	e.files.files[quote].readAt = time.Now().UTC().Truncate(time.Microsecond)
	before = e.prov.embedded
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if e.prov.embedded-before != 1 {
		t.Errorf("a re-read file: %d chunks embedded, want 1", e.prov.embedded-before)
	}

	// Another embedding model: everything is indexed again under it.
	_ = e.svc.SaveSettings(ctx, Settings{ChatProvider: &pid, ChatModel: "chat-m", EmbedProvider: &pid, EmbedModel: "embed-2"})
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.Status(ctx); st.Indexed != len(e.files.files)+len(e.notes.files) {
		t.Errorf("after a model change: %+v", st)
	}
	var old int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM assistant.chunks WHERE model = $1`, embedModelID(pid, "embed-m")).Scan(&old)
	if old != 0 {
		t.Errorf("%d vectors of the previous model remain", old)
	}

	// A source that is no longer registered: its part of the index goes.
	without := New(e.pool, e.svc.sealer, []knowledge.Source{e.files}, Options{Logger: e.svc.log})
	if err := without.sync(ctx); err != nil {
		t.Fatal(err)
	}
	var notesLeft int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM assistant.chunks WHERE source = 'notes'`).Scan(&notesLeft)
	if notesLeft != 0 {
		t.Errorf("%d chunks of an unregistered source remain", notesLeft)
	}

	// Embeddings off: no vectors stay behind.
	_ = e.svc.SaveSettings(ctx, Settings{ChatProvider: &pid, ChatModel: "chat-m"})
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM assistant.chunks`).Scan(&left)
	if left != 0 {
		t.Errorf("%d vectors remain with embeddings off", left)
	}
}

func TestProviderRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := newID()
	var input *InputError

	// Metadata and link-local addresses are never a provider.
	if _, err := e.svc.SaveProvider(ctx, admin, nil, ProviderInput{Name: "it-meta", BaseURL: "http://169.254.169.254/v1"}); !errors.As(err, &input) {
		t.Errorf("metadata address: %v", err)
	}
	// The platform's own services neither: they are refused at connect time.
	u, _ := url.Parse(e.prov.srv.URL)
	blocked := New(e.pool, e.svc.sealer, []knowledge.Source{e.files}, Options{Logger: e.svc.log, Services: []string{u.Host}})
	p, err := blocked.SaveProvider(ctx, admin, nil, ProviderInput{Name: "it-db", BaseURL: e.prov.srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	var pe *ProviderError
	if _, err := blocked.TestProvider(ctx, p.ID); !errors.As(err, &pe) || pe.Kind != "blocked" {
		t.Errorf("a platform service as provider: %v", err)
	}

	// The key is stored sealed and never returned; replacing works, reading does not.
	key := "sk-geheim-123"
	p, err = e.svc.SaveProvider(ctx, admin, nil, ProviderInput{Name: "it-key", BaseURL: e.prov.srv.URL + "/v1", Key: &key})
	if err != nil || !p.HasKey {
		t.Fatalf("with key: %+v %v", p, err)
	}
	var stored []byte
	_ = e.pool.QueryRow(ctx, `SELECT api_key FROM assistant.providers WHERE id = $1`, p.ID).Scan(&stored)
	if strings.Contains(string(stored), key) {
		t.Error("the key is stored readable")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), key) {
		t.Error("the key is returned")
	}
	if _, err := e.svc.SaveProvider(ctx, admin, nil, ProviderInput{Name: "it-bad-key", BaseURL: e.prov.srv.URL + "/v1", Key: new("met spatie")}); !errors.As(err, &input) {
		t.Errorf("key with a space: %v", err)
	}

	// A chat model as embedding model, or a model the provider does not have, is refused at once.
	if err := e.svc.SaveSettings(ctx, Settings{EmbedProvider: &p.ID, EmbedModel: "chat-m"}); !errors.As(err, &input) || !strings.Contains(err.Error(), "maakt geen embeddings") {
		t.Errorf("chat model for embeddings: %v", err)
	}
	if err := e.svc.SaveSettings(ctx, Settings{ChatProvider: &p.ID, ChatModel: "bestaat-niet"}); !errors.As(err, &input) {
		t.Errorf("unknown model: %v", err)
	}

	// Settings need a model with each provider; questions need a chat model.
	if err := e.svc.SaveSettings(ctx, Settings{ChatProvider: &p.ID}); !errors.As(err, &input) {
		t.Errorf("provider without model: %v", err)
	}
	_ = e.svc.SaveSettings(ctx, Settings{})
	user := e.user(t)
	conv, _ := e.svc.CreateConversation(ctx, user)
	if _, err := e.svc.Ask(ctx, user, conv.ID, "Hallo?", nil, Stream{Delta: func(string) {}}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("ask without a model: %v", err)
	}
}

func TestHTTP(t *testing.T) {
	e := newEnv(t)
	ann := e.user(t)
	e.files.files[newID()] = &fakeFile{name: "offerte.pdf", version: newID(), viewers: []uuid.UUID{ann},
		text: "De dakkapel kost 5000 euro inclusief plaatsing en afvoer."}
	e.configure(t, "embed-m")
	if err := e.svc.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	pass := func(next http.Handler) http.Handler { return next }
	deny := func(string) func(http.Handler) http.Handler {
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
		}
	}
	r := chi.NewRouter()
	r.Route("/api/v1/assistant", func(r chi.Router) {
		e.svc.Routes(r, Auth{RequireUser: pass, RequirePermission: deny, RequireRecentAuth: pass,
			UserID: func(*http.Request) uuid.UUID { return ann }, Audit: func(*http.Request, string, map[string]any) {}})
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Settings need the permission (denied here).
	if resp, _ := http.Get(srv.URL + "/api/v1/assistant/providers"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("providers without the permission: %d", resp.StatusCode)
	}
	resp, err := http.Post(srv.URL+"/api/v1/assistant/conversations", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create conversation: %v %v", resp, err)
	}
	var conv Conversation
	_ = json.NewDecoder(resp.Body).Decode(&conv)
	resp.Body.Close()

	resp, err = http.Post(srv.URL+"/api/v1/assistant/conversations/"+conv.ID.String()+"/messages", "application/json",
		strings.NewReader(`{"content":"Wat kost de dakkapel?"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	stream := string(body)
	if resp.Header.Get("Content-Type") != "text/event-stream" || strings.Count(stream, "event: delta") != 3 ||
		!strings.Contains(stream, "event: done") || !strings.Contains(stream, "offerte.pdf") {
		t.Fatalf("stream:\n%s", stream)
	}
	// An empty question is an ordinary error, before anything is streamed.
	resp, _ = http.Post(srv.URL+"/api/v1/assistant/conversations/"+conv.ID.String()+"/messages", "application/json",
		strings.NewReader(`{"content":"   "}`))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("empty question: %d", resp.StatusCode)
	}
}

func TestTools(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ann, bob := e.user(t), e.user(t)
	quote := newID()
	e.files.files[quote] = &fakeFile{name: "offerte.pdf", version: newID(), viewers: []uuid.UUID{ann},
		text: "Offerte voor de dakkapel. De dakkapel kost 5000 euro inclusief plaatsing."}
	e.configure(t, "embed-m")
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}

	// The model searches, reads the first result, then answers.
	conv, _ := e.svc.CreateConversation(ctx, ann)
	var steps []string
	resets := 0
	msg, err := e.svc.Ask(ctx, ann, conv.ID, "ZOEKTEST wat kost het?", nil, Stream{
		Delta: func(string) {}, Step: func(s string) { steps = append(steps, s) }, Reset: func() { resets++ }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, " | ") != "Zocht naar “dakkapel” | Las “offerte.pdf”" || strings.Join(msg.Steps, " | ") != strings.Join(steps, " | ") {
		t.Fatalf("steps: %v / stored %v", steps, msg.Steps)
	}
	if last := e.prov.prompts[len(e.prov.prompts)-1]; !strings.Contains(last, "inclusief plaatsing") {
		t.Errorf("the read text did not reach the model:\n%s", last)
	}
	if !slices.ContainsFunc(msg.Sources, func(s Source) bool { return s.ID == quote.String() }) {
		t.Errorf("the file found by the tool is not among the sources: %+v", msg.Sources)
	}
	if got, _ := e.svc.GetConversation(ctx, ann, conv.ID); len(got.Messages[1].Steps) != 2 {
		t.Errorf("steps not stored: %+v", got.Messages[1])
	}

	// Bob's search finds nothing of Ann's, so there is nothing to read.
	bobConv, _ := e.svc.CreateConversation(ctx, bob)
	if _, err := e.svc.Ask(ctx, bob, bobConv.ID, "ZOEKTEST wat kost het?", nil, Stream{Delta: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range e.prov.prompts[len(e.prov.prompts)-2:] {
		if strings.Contains(p, "offerte.pdf") || strings.Contains(p, "5000 euro inclusief") {
			t.Fatalf("ann's file reached bob through a tool:\n%s", p)
		}
	}

	// A number the model made up reads nothing.
	if _, err := e.svc.Ask(ctx, ann, conv.ID, "ONGELDIG", nil, Stream{Delta: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if last := e.prov.prompts[len(e.prov.prompts)-1]; !strings.Contains(last, "Onbekend of niet beschikbaar bronnummer") {
		t.Errorf("reading an unknown number:\n%s", last)
	}

	// A provider that refuses tools still answers, from what was found beforehand.
	e.prov.mu.Lock()
	e.prov.refuseTools = true
	e.prov.mu.Unlock()
	msg, err = e.svc.Ask(ctx, ann, conv.ID, "Wat kost de dakkapel?", nil, Stream{Delta: func(string) {}})
	if err != nil || msg.Content == "" || len(msg.Steps) != 0 {
		t.Fatalf("without tools: %+v %v", msg, err)
	}
	if last := e.prov.prompts[len(e.prov.prompts)-1]; strings.Contains(last, `"tools"`) || strings.Contains(last, "Hulpmiddelen:") {
		t.Errorf("tools were offered again after a refusal:\n%s", last)
	}
}
