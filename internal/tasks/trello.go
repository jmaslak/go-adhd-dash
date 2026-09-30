package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// defaultTrelloURL is where the Trello API lives, unless the configuration
// says otherwise.
const defaultTrelloURL = "https://trello.com/"

// trelloClient makes the few Trello API requests the dashboard needs.
type trelloClient struct {
	key, token, baseURL string
	http                *http.Client
}

// newTrelloClient returns a client for cfg's credentials, or an error when
// it has none.
func newTrelloClient(cfg Config) (*trelloClient, error) {
	if cfg.APIKey == "" || cfg.Token == "" {
		return nil, errors.New("no Trello api-key and token are configured in ~/.task.secret.yaml")
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultTrelloURL
	}
	return &trelloClient{key: cfg.APIKey, token: cfg.Token, baseURL: base, http: &http.Client{Timeout: 30 * time.Second}}, nil
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

// boardIDs are the IDs of the boards the credentials can see, by name.
func (c *trelloClient) boardIDs(ctx context.Context) (map[string]string, error) {
	boards, err := c.get(ctx, "1/members/me/boards", "name")
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, b := range boards {
		if _, dup := ids[b.Name]; dup {
			return nil, fmt.Errorf("two Trello boards are called %q", b.Name)
		}
		ids[b.Name] = b.ID
	}
	return ids, nil
}

// fetchTasks reads the open cards of each of lists, as tasks in the order of
// lists and then of the cards on each, numbered from 1, and the IDs of the
// lists.
func fetchTasks(ctx context.Context, c *trelloClient, lists []Destination) ([]Task, map[Destination]string, error) {
	boards, err := c.boardIDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	type board struct {
		lists []trelloItem
		cards []trelloItem
	}
	fetched := map[string]*board{}
	listIDs := map[Destination]string{}
	var out []Task
	for _, d := range lists {
		b, ok := fetched[d.Board]
		if !ok {
			id, ok := boards[d.Board]
			if !ok {
				return nil, nil, fmt.Errorf("there is no Trello board %q", d.Board)
			}
			b = &board{}
			if b.lists, err = c.get(ctx, "1/boards/"+url.PathEscape(id)+"/lists", "name"); err != nil {
				return nil, nil, err
			}
			if b.cards, err = c.get(ctx, "1/boards/"+url.PathEscape(id)+"/cards", "name,idList,pos"); err != nil {
				return nil, nil, err
			}
			fetched[d.Board] = b
		}
		i := slices.IndexFunc(b.lists, func(l trelloItem) bool { return l.Name == d.List })
		if i < 0 {
			return nil, nil, fmt.Errorf("there is no list %q on Trello board %q", d.List, d.Board)
		}
		listIDs[d] = b.lists[i].ID

		var cards []trelloItem
		for _, card := range b.cards {
			if card.IDList == b.lists[i].ID {
				cards = append(cards, card)
			}
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
			out = append(out, Task{Title: card.Name, Tags: []string{d.Tag}, CardID: card.ID, Dest: d})
		}
	}
	renumber(out)
	return out, listIDs, nil
}

// listID finds the ID of d's list.
func (c *trelloClient) listID(ctx context.Context, d Destination) (string, error) {
	boards, err := c.boardIDs(ctx)
	if err != nil {
		return "", err
	}
	id, ok := boards[d.Board]
	if !ok {
		return "", fmt.Errorf("there is no Trello board %q", d.Board)
	}
	lists, err := c.get(ctx, "1/boards/"+url.PathEscape(id)+"/lists", "name")
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(lists, func(l trelloItem) bool { return l.Name == d.List })
	if i < 0 {
		return "", fmt.Errorf("there is no list %q on Trello board %q", d.List, d.Board)
	}
	return lists[i].ID, nil
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
