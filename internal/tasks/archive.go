package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/jmaslak/go-task/config"
	"github.com/jmaslak/go-task/task"
	"github.com/jmaslak/go-task/trello"
)

// Archiver closes tasks as the task program does, through the task program's
// own store, so that the directory lock, the move into done/ and the
// renumbering of the tasks left behind are its, not a copy of them.
type Archiver struct {
	store *task.Store

	// loadConfig reads the task program's configuration, for its Trello
	// credentials. It is read at each use, as the ignored tags are, so a
	// change shows up without restarting.
	loadConfig func() (*config.Config, error)
}

// NewArchiver returns an archiver for the task directory dir, reading the
// Trello credentials from the task program's configuration files.
func NewArchiver(dir string) *Archiver {
	return &Archiver{
		store:      task.NewStore(dir),
		loadConfig: func() (*config.Config, error) { return config.Load(config.DefaultPaths()) },
	}
}

// Check reports why ts cannot all be archived, before anything is changed:
// a task with no ID yet cannot be found again safely once numbers shift, and
// one mirroring a Trello card needs Trello credentials to close the card.
func (a *Archiver) Check(ts []Task) error {
	needTrello := false
	for _, t := range ts {
		if t.ID == nil {
			return fmt.Errorf("task %d has no task ID yet; run the task program once to upgrade it", t.Number)
		}
		needTrello = needTrello || t.TrelloID != ""
	}
	if needTrello {
		if _, err := a.trello(); err != nil {
			return err
		}
	}
	return nil
}

// Archive closes t. A task mirroring a Trello card has the card marked done
// and archived first: were the card left open, the next Trello sync would
// bring the task back. The task is then found by its ID, since its number may
// have changed since it was shown, moved into done/, and the tasks after it
// renumbered, under the task directory's lock.
func (a *Archiver) Archive(ctx context.Context, t Task) error {
	if t.TrelloID != "" {
		client, err := a.trello()
		if err != nil {
			return err
		}
		if err := client.CloseCard(ctx, t.TrelloID); err != nil {
			return fmt.Errorf("closing its Trello card: %w", err)
		}
	}
	if err := a.store.ArchiveByID(t.ID); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			return errors.New("it is no longer open")
		}
		return err
	}
	return nil
}

// trello returns a client for the configured Trello credentials.
func (a *Archiver) trello() (*trello.Client, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("reading the task configuration: %w", err)
	}
	if cfg.Trello.APIKey == "" || cfg.Trello.Token == "" {
		return nil, errors.New("a marked task mirrors a Trello card, but no Trello api-key and token are configured in ~/.task.secret.yaml")
	}
	return trello.New(cfg.Trello.APIKey, cfg.Trello.Token, cfg.Trello.BaseURL), nil
}
