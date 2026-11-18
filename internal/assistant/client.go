package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProviderError is a failure talking to a provider, in words safe to show.
// What the provider said exactly goes to the log, never to the page: it
// can contain internal details (ADR 0023).
type ProviderError struct {
	Kind   string // unreachable, blocked, unauthorized, incompatible, rate_limited, failed
	detail error
}

func (e *ProviderError) Error() string {
	switch e.Kind {
	case "blocked":
		return "Dit adres is niet toegestaan."
	case "unreachable":
		return "De aanbieder is niet bereikbaar."
	case "unauthorized":
		return "De aanbieder weigert de API-sleutel."
	case "incompatible":
		return "Dit is geen OpenAI-compatibele API, of het model bestaat niet."
	case "rate_limited":
		return "De aanbieder weigert tijdelijk: te veel verzoeken."
	}
	return "De aanbieder gaf een fout."
}

// Detail is the underlying error, for the log only.
func (e *ProviderError) Detail() error { return e.detail }

const (
	maxModelsBody = 1 << 20  // a model list
	maxEmbedBody  = 64 << 20 // embeddings of one batch
	maxChatLine   = 1 << 20  // one streamed event
	maxAnswer     = 64 << 10 // characters of one answer
)

// client talks to one OpenAI-compatible provider.
type client struct {
	base *url.URL
	key  string
	http *http.Client
}

func newClient(g *guard, base *url.URL, key string, external bool) *client {
	transport := &http.Transport{
		Proxy:                 nil, // never through a proxy from the environment
		DialContext:           g.dialer(external),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute, // a local model may take a while to load
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       time.Minute,
		ForceAttemptHTTP2:     true,
	}
	return &client{base: base, key: key, http: &http.Client{
		Transport: transport,
		// Redirects could lead anywhere, past the address checks' intent.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	u := *c.base
	u.Path += path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return nil, &ProviderError{Kind: "failed", detail: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) || errors.Is(err, errExternal) {
			return nil, &ProviderError{Kind: "blocked", detail: err}
		}
		return nil, &ProviderError{Kind: "unreachable", detail: err}
	}
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		err := fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(detail)))
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, &ProviderError{Kind: "unauthorized", detail: err}
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, &ProviderError{Kind: "rate_limited", detail: err}
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 3:
			return nil, &ProviderError{Kind: "incompatible", detail: err}
		}
		return nil, &ProviderError{Kind: "failed", detail: err}
	}
	return resp, nil
}

func decode(resp *http.Response, limit int64, out any) error {
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(out); err != nil {
		return &ProviderError{Kind: "incompatible", detail: err}
	}
	return nil
}

// Models lists the models the provider offers (GET /models).
func (c *client) Models(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := decode(resp, maxModelsBody, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" && len(m.ID) <= 200 {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// Embed returns one vector per input (POST /embeddings), in input order.
func (c *client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	resp, err := c.do(ctx, http.MethodPost, "/embeddings", map[string]any{"model": model, "input": inputs})
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := decode(resp, maxEmbedBody, &out); err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(inputs))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(inputs) || len(d.Embedding) == 0 || len(d.Embedding) > 16000 {
			return nil, &ProviderError{Kind: "incompatible", detail: fmt.Errorf("embedding %d of %d values", d.Index, len(d.Embedding))}
		}
		vectors[d.Index] = d.Embedding
	}
	for i, v := range vectors {
		if v == nil || len(v) != len(vectors[0]) {
			return nil, &ProviderError{Kind: "incompatible", detail: fmt.Errorf("embedding %d missing or of another size", i)}
		}
	}
	return vectors, nil
}

// message is one turn sent to a chat model.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Chat streams an answer (POST /chat/completions with stream), calling
// delta for each piece of text as it arrives. It returns the whole answer.
func (c *client) Chat(ctx context.Context, model string, messages []message, delta func(string)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", map[string]any{
		"model": model, "messages": messages, "stream": true, "temperature": 0.2,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var answer strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxChatLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // comments, event names, keep-alives
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return answer.String(), &ProviderError{Kind: "incompatible", detail: err}
		}
		for _, ch := range ev.Choices {
			if ch.Delta.Content == "" {
				continue
			}
			if answer.Len()+len(ch.Delta.Content) > maxAnswer {
				return answer.String(), nil // long enough; stop rather than store unbounded text
			}
			answer.WriteString(ch.Delta.Content)
			delta(ch.Delta.Content)
		}
	}
	if err := sc.Err(); err != nil {
		return answer.String(), &ProviderError{Kind: "unreachable", detail: err}
	}
	return answer.String(), nil
}
