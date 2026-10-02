package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// DefaultBaseURL is where the Trello API lives; tests point a Config's
// BaseURL elsewhere.
const DefaultBaseURL = "https://trello.com/"

// AuthorizeEndpoint is Trello's page for granting an app a token; tests
// point it elsewhere.
var AuthorizeEndpoint = "https://trello.com/1/authorize"

// AuthorizeURL is where to send a browser for its user to grant the app
// appName, whose API key is key, a token to read and write their boards,
// that does not expire. Trello sends the browser back to returnURL with
// the token in its fragment, #token=..., which the server never sees; the
// page there reads it out.
func AuthorizeURL(key, appName, returnURL string) string {
	return AuthorizeEndpoint + "?" + url.Values{
		"key":             {key},
		"name":            {appName},
		"scope":           {"read,write"},
		"expiration":      {"never"},
		"response_type":   {"token"},
		"callback_method": {"fragment"},
		"return_url":      {returnURL},
	}.Encode()
}

// trelloClient makes the few Trello API requests the dashboard needs.
type trelloClient struct {
	key, token, baseURL string
	http                *http.Client
}

// newTrelloClient returns a client for cfg's credentials, or an error when
// it has none.
func newTrelloClient(cfg Config) (*trelloClient, error) {
	if cfg.APIKey == "" || cfg.Token == "" {
		return nil, errors.New("no Trello authorization")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &trelloClient{key: cfg.APIKey, token: cfg.Token, baseURL: base, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Board is one of a user's open Trello boards, with its open lists.
type Board struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Lists []List `json:"lists"`
}

// List is a list on a Trello board.
type List struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Boards are the open boards cfg's token can see, each with its open lists,
// in Trello's order.
func Boards(ctx context.Context, cfg Config) ([]Board, error) {
	c, err := newTrelloClient(cfg)
	if err != nil {
		return nil, err
	}
	var boards []Board
	q := url.Values{"filter": {"open"}, "fields": {"name"}, "lists": {"open"}, "list_fields": {"name"}}
	err = c.do(ctx, http.MethodGet, "1/members/me/boards", q, &boards)
	return boards, err
}

// Member is the user name of the Trello member cfg's token is for, which
// checks that the token works.
func Member(ctx context.Context, cfg Config) (string, error) {
	c, err := newTrelloClient(cfg)
	if err != nil {
		return "", err
	}
	var m struct {
		Username string `json:"username"`
	}
	if err := c.do(ctx, http.MethodGet, "1/members/me", url.Values{"fields": {"username"}}, &m); err != nil {
		return "", err
	}
	if m.Username == "" {
		return "", errors.New("trello: 1/members/me: no user name in the reply")
	}
	return m.Username, nil
}

// Revoke withdraws cfg's token at Trello.
func Revoke(ctx context.Context, cfg Config) error {
	c, err := newTrelloClient(cfg)
	if err != nil {
		return err
	}
	var out struct{}
	if err := c.do(ctx, http.MethodDelete, "1/tokens/"+url.PathEscape(cfg.Token), url.Values{}, &out); err != nil {
		// The path is the token: keep it out of the message.
		return errors.New(strings.ReplaceAll(err.Error(), url.PathEscape(cfg.Token), "<token>"))
	}
	return nil
}

// trelloItem is a board, list or card: the fields of each the dashboard
// reads.
type trelloItem struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	IDList string  `json:"idList"`
	Pos    float64 `json:"pos"`
	Closed bool    `json:"closed"`
}

// do makes one request, with query and the credentials, decoding the JSON
// reply into out.
func (c *trelloClient) do(ctx context.Context, method, path string, query url.Values, out any) error {
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("invalid Trello base URL: %w", err)
	}
	endpoint = endpoint.JoinPath(path)
	query.Set("key", c.key)
	query.Set("token", c.token)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error names the URL, credentials and all.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("trello: %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("trello: %s: %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("trello: %s: %w", path, err)
	}
	return nil
}

// get fetches path's fields.
func (c *trelloClient) get(ctx context.Context, path, fields string) ([]trelloItem, error) {
	var out []trelloItem
	err := c.do(ctx, http.MethodGet, path, url.Values{"fields": {fields}}, &out)
	return out, err
}

// fetchTasks reads the open cards of each of lists, as tasks in the order of
// lists and then of the cards on each, numbered from 1.
func fetchTasks(ctx context.Context, c *trelloClient, lists []Destination) ([]Task, error) {
	var out []Task
	for _, d := range lists {
		cards, err := c.get(ctx, "1/lists/"+url.PathEscape(d.ListID)+"/cards", "name,pos")
		if err != nil {
			return nil, fmt.Errorf("the list %s: %w", d, err)
		}
		slices.SortStableFunc(cards, func(x, y trelloItem) int {
			switch {
			case x.Pos < y.Pos:
				return -1
			case x.Pos > y.Pos:
				return 1
			}
			return 0
		})
		for _, card := range cards {
			out = append(out, Task{Title: card.Name, Tags: d.tags(), CardID: card.ID, Dest: d})
		}
	}
	renumber(out)
	return out, nil
}

// createCard adds a card named name at the bottom of list listID, returning
// its ID.
func (c *trelloClient) createCard(ctx context.Context, listID, name string) (string, error) {
	var card trelloItem
	query := url.Values{"idList": {listID}, "name": {name}, "pos": {"bottom"}}
	if err := c.do(ctx, http.MethodPost, "1/cards", query, &card); err != nil {
		return "", err
	}
	if card.ID == "" {
		return "", errors.New("trello: 1/cards: no card ID in the reply")
	}
	return card.ID, nil
}

// closeCard marks a card's due date complete and archives the card, as is
// done to a card whose task is finished.
func (c *trelloClient) closeCard(ctx context.Context, cardID string) error {
	var card trelloItem
	query := url.Values{"dueComplete": {"true"}, "closed": {"true"}, "fields": {"closed"}}
	path := "1/cards/" + url.PathEscape(cardID)
	if err := c.do(ctx, http.MethodPut, path, query, &card); err != nil {
		return err
	}
	if !card.Closed {
		return fmt.Errorf("trello: %s: the card was not archived", path)
	}
	return nil
}
