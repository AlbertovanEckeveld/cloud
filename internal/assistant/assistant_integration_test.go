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
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
	"github.com/AlbertovanEckeveld/cloud/internal/drive"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

// fakeFiles stands in for drive: files with text, an owner, and who else
// may see them.
type fakeFiles struct {
	mu    sync.Mutex
	files map[uuid.UUID]*fakeFile
}

type fakeFile struct {
	name    string
	version uuid.UUID
	text    string
	viewers []uuid.UUID
}

func (f *fakeFiles) TextIndex(_ context.Context, after uuid.UUID, limit int) ([]drive.TextRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []drive.TextRef
	for id, file := range f.files {
		if id.String() > after.String() {
			out = append(out, drive.TextRef{NodeID: id, VersionID: file.version})
		}
	}
	slices.SortFunc(out, func(a, b drive.TextRef) int { return strings.Compare(a.NodeID.String(), b.NodeID.String()) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeFiles) TextCount(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.files), nil
}

func (f *fakeFiles) Text(_ context.Context, id uuid.UUID) (drive.TextRef, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[id]
	if !ok {
		return drive.TextRef{}, "", drive.ErrNotFound
	}
	return drive.TextRef{NodeID: id, VersionID: file.version}, file.text, nil
}

func (f *fakeFiles) result(id uuid.UUID, file *fakeFile) drive.SearchResult {
	var r drive.SearchResult
	r.ID, r.Name, r.Kind, r.Location = id, file.name, "file", "Mijn bestanden"
	return r
}

func (f *fakeFiles) Visible(_ context.Context, user uuid.UUID, ids []uuid.UUID) ([]drive.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []drive.SearchResult{}
	for _, id := range ids {
		if file, ok := f.files[id]; ok && slices.Contains(file.viewers, user) {
			out = append(out, f.result(id, file))
		}
	}
	return out, nil
}

func (f *fakeFiles) Search(_ context.Context, user uuid.UUID, q string) ([]drive.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []drive.SearchResult{}
	for id, file := range f.files {
		if slices.Contains(file.viewers, user) && strings.Contains(strings.ToLower(file.name), strings.ToLower(q)) {
			out = append(out, f.result(id, file))
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
	srv      *httptest.Server
	mu       sync.Mutex
	prompts  []string
	embedded int
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
			p.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
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
	files *fakeFiles
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
	files := &fakeFiles{files: map[uuid.UUID]*fakeFile{}}
	e := &env{svc: New(pool, sl, files, Options{Logger: logger}), pool: pool, files: files, prov: newProvider(t)}

	// The settings row is shared: put it back afterwards.
	before, err := e.svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `UPDATE assistant.settings SET chat_provider = $1, chat_model = NULLIF($2, ''),
			embed_provider = $3, embed_model = NULLIF($4, '')`, before.ChatProvider, before.ChatModel, before.EmbedProvider, before.EmbedModel)
		for id := range files.files {
			_, _ = pool.Exec(ctx, `DELETE FROM assistant.chunks WHERE node_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM assistant.documents WHERE node_id = $1`, id)
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

	pid := e.configure(t, "embed-m")
	if models, err := e.svc.TestProvider(ctx, pid); err != nil || len(models) != 3 {
		t.Fatalf("test provider: %v %v", models, err)
	}
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.Status(ctx); !st.Configured || st.Files != 3 || st.Indexed != 3 || st.Failed != 0 {
		t.Fatalf("status: %+v", st)
	}

	// Search by meaning: only what the user may see.
	results, err := e.svc.Search(ctx, ann, "wat kost de dakkapel")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].ID != quote || slices.ContainsFunc(results, func(r drive.SearchResult) bool { return r.ID == salary }) {
		t.Fatalf("search for ann: %+v", results)
	}

	// Chat: the model only reads what the user may see.
	conv, err := e.svc.CreateConversation(ctx, ann)
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	msg, err := e.svc.Ask(ctx, ann, conv.ID, "Wat kost de dakkapel?", func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	prompt := e.prov.prompts[len(e.prov.prompts)-1]
	if !strings.Contains(prompt, "5000 euro inclusief plaatsing") || strings.Contains(prompt, "salaris") {
		t.Fatalf("prompt for ann:\n%s", prompt)
	}
	if streamed.String() != msg.Content || msg.Content != "Volgens [1] kost het 5000 euro." ||
		len(msg.Sources) == 0 || msg.Sources[0].ID != quote || msg.Sources[0].N != 1 {
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
	if _, err := e.svc.Ask(ctx, bob, conv.ID, "?", func(string) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob asks in ann's conversation: %v", err)
	}
	// Losing access hides the source later too.
	e.files.files[quote].viewers = nil
	got, _ = e.svc.GetConversation(ctx, ann, conv.ID)
	if slices.ContainsFunc(got.Messages[1].Sources, func(s Source) bool { return s.ID == quote }) {
		t.Errorf("a source ann can no longer open is still listed: %+v", got.Messages[1].Sources)
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
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM assistant.chunks WHERE node_id = $1`, salary).Scan(&n)
	if n != 0 {
		t.Errorf("a removed file still has %d chunks", n)
	}

	// Another embedding model: everything is indexed again under it.
	_ = e.svc.SaveSettings(ctx, Settings{ChatProvider: &pid, ChatModel: "chat-m", EmbedProvider: &pid, EmbedModel: "embed-2"})
	if err := e.svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.Status(ctx); st.Indexed != len(e.files.files) {
		t.Errorf("after a model change: %+v", st)
	}
	var old int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM assistant.chunks WHERE model = $1`, embedModelID(pid, "embed-m")).Scan(&old)
	if old != 0 {
		t.Errorf("%d vectors of the previous model remain", old)
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
	blocked := New(e.pool, e.svc.sealer, e.files, Options{Logger: e.svc.log, Services: []string{u.Host}})
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
	if _, err := e.svc.Ask(ctx, user, conv.ID, "Hallo?", func(string) {}); !errors.Is(err, ErrNotConfigured) {
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
