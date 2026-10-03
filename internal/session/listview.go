package session

import (
	"context"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// TaskBackend is everything the task screens need of a user's Trello:
// their tasks, any list's cards, and the changes to make to them.
// *tasks.Cache is the real one.
type TaskBackend interface {
	TaskSource
	TaskAdder

	// Cards are the open cards on a list, as tasks numbered from 1.
	Cards(ctx context.Context, d tasks.Destination) ([]tasks.Task, error)
}

// listViewTTL is how long a list's cards are shown before they are read
// again; any change made to them reads them again at once.
const listViewTTL = time.Minute

// listView is one Trello list's cards as a task screen's tasks: a
// TaskSource whose changes go through backend, as the user's tasks' do.
type listView struct {
	backend TaskBackend
	dest    tasks.Destination

	snap  tasks.Snapshot
	stale bool // read the cards again at the next Snapshot
	now   func() time.Time
}

// newListView shows dest's cards, read and changed through backend.
func newListView(backend TaskBackend, dest tasks.Destination) *listView {
	return &listView{backend: backend, dest: dest, stale: true, now: time.Now}
}

// Snapshot is the list's cards, read again if they have changed or are
// older than listViewTTL. A failed read keeps the cards read before.
func (v *listView) Snapshot() tasks.Snapshot {
	if v.stale || v.now().Sub(v.snap.Fetched) > listViewTTL {
		ctx, cancel := context.WithTimeout(context.Background(), taskChangeTimeout)
		defer cancel()
		cards, err := v.backend.Cards(ctx, v.dest)
		if err != nil {
			v.snap.Err = err
		} else {
			v.snap = tasks.Snapshot{Tasks: cards, Fetched: v.now()}
		}
		v.stale = false
	}
	return v.snap
}

// changed marks the cards to be read again, whatever err, since a failure
// may have come after Trello made the change.
func (v *listView) changed(err error) error {
	v.stale = true
	return err
}

func (v *listView) Boards(ctx context.Context) ([]tasks.Board, error) { return v.backend.Boards(ctx) }

func (v *listView) Destinations() ([]tasks.Destination, error) { return v.backend.Destinations() }

func (v *listView) Archive(ctx context.Context, t tasks.Task) error {
	return v.changed(v.backend.Archive(ctx, t))
}

func (v *listView) Move(ctx context.Context, t tasks.Task, d tasks.Destination) error {
	return v.changed(v.backend.Move(ctx, t, d))
}

func (v *listView) Rename(ctx context.Context, t tasks.Task, title string) error {
	return v.changed(v.backend.Rename(ctx, t, title))
}

func (v *listView) Reposition(ctx context.Context, t tasks.Task, pos string) error {
	return v.changed(v.backend.Reposition(ctx, t, pos))
}

// listAdder adds tasks to the list a listView shows: the add-task screen's
// only board.
type listAdder struct{ v *listView }

func (a listAdder) Destinations() ([]tasks.Destination, error) {
	return []tasks.Destination{a.v.dest}, nil
}

// Add adds a card at the bottom of the list, returning its number there.
func (a listAdder) Add(ctx context.Context, title string, d tasks.Destination) (int, error) {
	n := len(a.v.Snapshot().Tasks) + 1
	if _, err := a.v.backend.Add(ctx, title, d); err != nil {
		return 0, a.v.changed(err)
	}
	return n, a.v.changed(nil)
}
