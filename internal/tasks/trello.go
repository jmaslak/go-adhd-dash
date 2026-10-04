package tasks

import (
	"cmp"
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
			out = append(out, Task{Title: card.Name, Tags: d.tags(), CardID: card.ID, Dest: d, Pos: card.Pos})
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

// moveCard moves a card to the bottom of list listID, on board boardID,
// which may be another board than its own.
func (c *trelloClient) moveCard(ctx context.Context, cardID, boardID, listID string) error {
	var card trelloItem
	query := url.Values{"idBoard": {boardID}, "idList": {listID}, "pos": {"bottom"}, "fields": {"idList"}}
	path := "1/cards/" + url.PathEscape(cardID)
	if err := c.do(ctx, http.MethodPut, path, query, &card); err != nil {
		return err
	}
	if card.IDList != listID {
		return fmt.Errorf("trello: %s: the card was not moved", path)
	}
	return nil
}

// renameCard sets a card's title.
func (c *trelloClient) renameCard(ctx context.Context, cardID, name string) error {
	var card trelloItem
	path := "1/cards/" + url.PathEscape(cardID)
	if err := c.do(ctx, http.MethodPut, path, url.Values{"name": {name}, "fields": {"name"}}, &card); err != nil {
		return err
	}
	if card.Name != name {
		return fmt.Errorf("trello: %s: the card was not renamed", path)
	}
	return nil
}

// positionCard moves a card within its list to pos ("top", "bottom" or a
// number), returning the position it took.
func (c *trelloClient) positionCard(ctx context.Context, cardID, pos string) (float64, error) {
	var card trelloItem
	path := "1/cards/" + url.PathEscape(cardID)
	if err := c.do(ctx, http.MethodPut, path, url.Values{"pos": {pos}, "fields": {"pos"}}, &card); err != nil {
		return 0, err
	}
	return card.Pos, nil
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

// Details are what a card holds beyond its title, to look at: its notes
// (Trello's description), due date, labels, checklists and comments.
type Details struct {
	Title string
	Notes string

	// Due is when the card is due, zero for no due date; DueComplete is
	// set once it is marked complete.
	Due         time.Time
	DueComplete bool

	Labels     []string // by name, or a label without one by its color
	Checklists []Checklist
	Comments   []Comment // newest first
}

// Checklist is one of a card's checklists, its items in order.
type Checklist struct {
	Name  string
	Items []CheckItem
}

// CheckItem is one item of a checklist.
type CheckItem struct {
	Name string
	Done bool
}

// Comment is a comment on a card.
type Comment struct {
	Author string
	Date   time.Time
	Text   string
}

// maxComments is how many of a card's comments, the newest, are read.
const maxComments = 20

// cardDetails is a card as 1/cards/{id} gives it, with its checklists and
// comments.
type cardDetails struct {
	Name        string    `json:"name"`
	Desc        string    `json:"desc"`
	Due         time.Time `json:"due"`
	DueComplete bool      `json:"dueComplete"`
	Labels      []struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"labels"`
	Checklists []wireChecklist `json:"checklists"`
	Actions    []struct {
		Date time.Time `json:"date"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
		MemberCreator struct {
			FullName string `json:"fullName"`
			Username string `json:"username"`
		} `json:"memberCreator"`
	} `json:"actions"`
}

// wireChecklist and wireCheckItem are a checklist and its items as the API
// gives them, each with its position, to put them in order.
type wireChecklist struct {
	Name       string          `json:"name"`
	Pos        float64         `json:"pos"`
	CheckItems []wireCheckItem `json:"checkItems"`
}

type wireCheckItem struct {
	Name  string  `json:"name"`
	State string  `json:"state"`
	Pos   float64 `json:"pos"`
}

// cardDetails reads a card's details.
func (c *trelloClient) cardDetails(ctx context.Context, cardID string) (Details, error) {
	var card cardDetails
	query := url.Values{
		"fields":           {"name,desc,due,dueComplete,labels"},
		"checklists":       {"all"},
		"checklist_fields": {"name,pos"},
		"actions":          {"commentCard"},
		"actions_limit":    {fmt.Sprint(maxComments)},
	}
	if err := c.do(ctx, http.MethodGet, "1/cards/"+url.PathEscape(cardID), query, &card); err != nil {
		return Details{}, err
	}
	d := Details{Title: card.Name, Notes: card.Desc, Due: card.Due, DueComplete: card.DueComplete}
	for _, l := range card.Labels {
		d.Labels = append(d.Labels, cmp.Or(l.Name, l.Color))
	}
	slices.SortStableFunc(card.Checklists, func(a, b wireChecklist) int { return cmp.Compare(a.Pos, b.Pos) })
	for _, cl := range card.Checklists {
		slices.SortStableFunc(cl.CheckItems, func(a, b wireCheckItem) int { return cmp.Compare(a.Pos, b.Pos) })
		list := Checklist{Name: cl.Name}
		for _, it := range cl.CheckItems {
			list.Items = append(list.Items, CheckItem{Name: it.Name, Done: it.State == "complete"})
		}
		d.Checklists = append(d.Checklists, list)
	}
	for _, a := range card.Actions {
		d.Comments = append(d.Comments, Comment{Author: cmp.Or(a.MemberCreator.FullName, a.MemberCreator.Username), Date: a.Date, Text: a.Data.Text})
	}
	return d, nil
}
