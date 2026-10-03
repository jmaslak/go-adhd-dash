package session

import (
	"context"
	"fmt"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// TaskMover moves tasks for the move-task screen. *tasks.Cache is the real
// one.
type TaskMover interface {
	ListSource

	// Move moves one task's card to the bottom of a list.
	Move(ctx context.Context, t tasks.Task, d tasks.Destination) error
}

// moveTaskTimeout bounds moving one task.
const moveTaskTimeout = time.Minute

// movePrompt says how to pick where to move tasks.
const movePrompt = "Type any character beside the list to move them to, then press Enter."

// moveTaskState is one session's move-task screen: the tasks being moved,
// and the lists they can go to, one of which once picked and Enter pressed
// puts the confirmation up in place of the lists.
type moveTaskState struct {
	moving []tasks.Task
	listPicker

	// confirming is set once a list is picked and Enter pressed, until PF4
	// moves the tasks or PF3 goes back to the lists.
	confirming bool

	message string
	isError bool
}

// startMove begins moving ts, reading where they can go from mover.
func startMove(mover TaskMover, ts []tasks.Task) moveTaskState {
	return moveTaskState{moving: ts, listPicker: loadListPicker(mover)}
}

// buildMoveTask renders the move-task screen, the lists or the
// confirmation, and where the cursor goes: the first list's selection
// field, or the bottom row.
func buildMoveTask(rows, cols int, now time.Time, m *moveTaskState) (screen go3270.Screen, cursorRow, cursorCol int) {
	if m.confirming {
		return buildMoveConfirm(rows, cols, now, m), rows - 1, 0
	}
	screen = titleFields(cols, "MOVE TASKS", now)
	lists, cursorRow, cursorCol := m.build(rows, cols, "Move "+countText(len(m.moving), "task", 0, 1)+" to which list?")
	screen = append(screen, lists...)

	message, color := m.message, go3270.Red
	if message == "" {
		message, color = movePrompt, go3270.Blue
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: m.isError}})...)
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF7=Up PF8=Down Enter=Move to the list selected", cols-1)})
	return screen, cursorRow, cursorCol
}

// buildMoveConfirm renders the confirmation for moving the tasks to the
// list picked.
func buildMoveConfirm(rows, cols int, now time.Time, m *moveTaskState) go3270.Screen {
	d, _ := m.pickedDest()
	screen := titleFields(cols, "MOVE TASKS", now)
	screen = append(screen, placeLine(taskHeaderRow, cols, line{
		{Content: "Move " + countText(len(m.moving), "task", 0, 1) + " to", Color: go3270.Yellow, Intense: true},
		{Content: d.String() + "?", Color: go3270.White, Intense: true},
	})...)

	// The tasks run from below the heading to above the note.
	limit := max(rows-3-(taskHeaderRow+2), 1)
	shown := m.moving
	if len(shown) > limit {
		shown = shown[:limit-1]
	}
	row := taskHeaderRow + 2
	for _, t := range shown {
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, taskLine(t))...)
		row++
	}
	if len(shown) < len(m.moving) {
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, line{{Content: fmt.Sprintf("... and %d more", len(m.moving)-len(shown)), Color: go3270.Blue}})...)
	}

	note := "Their cards go to the bottom of that list, tagged [" + d.Tag + "]."
	if d.Tag == "" {
		note = "Their cards go to the bottom of that list, whose tasks are not shown here."
	}
	screen = append(screen, placeLine(rows-3, cols, line{{Content: note, Color: go3270.Turquoise}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to move them, or PF3 to pick another list.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Move", cols-1)})
}

// handle acts on a key on the move-task screen, returning whether to go
// back to the task list, and what to say there: how many tasks moved, or
// why one did not. On the lists, PF3 goes back with nothing moved, and
// Enter asks to confirm the list selected; on the confirmation, PF3 goes
// back to the lists and PF4 moves the tasks, stopping at the first that
// fails. Each task moved has its mark taken from tp.
func (m *moveTaskState) handle(resp go3270.Response, mover TaskMover, tp *taskPageState, logf func(string, ...any)) (back bool, message string, isError bool) {
	m.message, m.isError = "", false
	if m.confirming {
		switch resp.AID {
		case go3270.AIDPF3:
			m.confirming = false
			return false, "", false
		case go3270.AIDPF4:
			message, isError = m.move(mover, tp, logf)
			return true, message, isError
		}
		return false, "", false
	}
	if resp.AID == go3270.AIDPF3 {
		return true, "", false
	}
	if bad := m.listPicker.handle(resp); bad != "" {
		m.message, m.isError = bad, true
		return false, "", false
	}
	if resp.AID == go3270.AIDEnter {
		if _, ok := m.pickedDest(); !ok {
			m.message, m.isError = movePrompt, true
			return false, "", false
		}
		m.confirming = true
	}
	return false, "", false
}

// move moves the tasks to the list picked, stopping at the first that
// fails, returning what to say on the task list. Each task moved has its
// mark taken from tp.
func (m *moveTaskState) move(mover TaskMover, tp *taskPageState, logf func(string, ...any)) (message string, isError bool) {
	d, _ := m.pickedDest()
	done := 0
	for _, t := range m.moving {
		ctx, cancel := context.WithTimeout(context.Background(), moveTaskTimeout)
		err := mover.Move(ctx, t, d)
		cancel()
		if err != nil {
			logf("moving task %d %q to %s: %v", t.Number, t.Title, d, err)
			return fmt.Sprintf("Moved %d of %d. Task %d, %q, was not moved: %v", done, len(m.moving), t.Number, t.Title, err), true
		}
		logf("moved task %d %q to %s", t.Number, t.Title, d)
		delete(tp.marked, t.CardID)
		done++
	}
	return fmt.Sprintf("Moved %s to %s.", countText(done, "task", 0, 1), d), false
}
