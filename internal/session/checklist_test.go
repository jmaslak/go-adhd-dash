package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/checklist"
)

// checklistRig drives the checklist screens as a session does: draw, then
// answer with every input field as drawn except the ones typed over.
type checklistRig struct {
	t      *testing.T
	store  *checklist.Store
	c      checklistState
	screen go3270.Screen
	rows   []string
	crow   int
}

func newChecklistRig(t *testing.T) *checklistRig {
	return newChecklistRigOn(t, checklist.NewStore(filepath.Join(t.TempDir(), "cl.json")))
}

// newChecklistRigOn is a rig on store, which may be another rig's, as
// another session's would be.
func newChecklistRigOn(t *testing.T, store *checklist.Store) *checklistRig {
	r := &checklistRig{t: t, store: store}
	r.draw()
	return r
}

func (r *checklistRig) draw() {
	r.t.Helper()
	lists, err := r.store.Load()
	r.screen, r.crow, _ = buildChecklist(24, 80, now, lists, err, &r.c)
	r.rows = screenText(r.t, r.screen, 24, 80)
}

// key presses aid with the cursor on row, having typed typed, and redraws,
// returning whether the screens were left.
func (r *checklistRig) key(aid go3270.AID, row int, typed map[string]string) bool {
	r.t.Helper()
	return r.keyAt(aid, row, 0, typed)
}

// keyAt is key with the cursor in column col.
func (r *checklistRig) keyAt(aid go3270.AID, row, col int, typed map[string]string) bool {
	r.t.Helper()
	values := map[string]string{}
	for _, f := range r.screen {
		if f.Write {
			values[f.Name] = f.Content
		}
	}
	for k, v := range typed {
		values[k] = v
	}
	leave := r.c.handle(go3270.Response{AID: aid, Row: row, Col: col, Values: values}, r.store)
	r.draw()
	return leave
}

func (r *checklistRig) lists() []checklist.Checklist {
	r.t.Helper()
	lists, err := r.store.Load()
	if err != nil {
		r.t.Fatal(err)
	}
	return lists
}

func TestChecklistList(t *testing.T) {
	r := newChecklistRig(t)
	if !strings.HasPrefix(r.rows[clHeaderRow], " CHECKLISTS 0 checklists") || r.rows[clColumnRow] != "" {
		t.Errorf("empty list: header %q, headings %q", r.rows[clHeaderRow], r.rows[clColumnRow])
	}
	if r.crow != 21 || !strings.HasPrefix(r.rows[21], " New checklist ===>") {
		t.Errorf("empty list: cursor on row %d, row 21 %q; want on the new checklist row", r.crow, r.rows[21])
	}

	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "Morning"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "Evening"})
	if l := r.lists(); len(l) != 2 || l[0].Name != "Morning" || l[1].Name != "Evening" {
		t.Fatalf("after adding: %+v", l)
	}
	if r.crow != 21 {
		t.Errorf("after adding, cursor on row %d; want still on the new checklist row", r.crow)
	}
	if !strings.HasPrefix(r.rows[clColumnRow], " S Name") || !strings.HasSuffix(r.rows[clColumnRow], "Done") {
		t.Errorf("headings %q", r.rows[clColumnRow])
	}
	if !strings.HasPrefix(r.rows[clFirstRow], "   Morning") || !strings.HasSuffix(r.rows[clFirstRow], " 0/0") {
		t.Errorf("first row %q", r.rows[clFirstRow])
	}
	// Names are protected, so that tabbing goes down the selection fields.
	for _, f := range r.screen {
		if f.Write && strings.HasPrefix(f.Name, clNameField) {
			t.Errorf("name field %q can be typed in", f.Name)
		}
	}

	// With nothing selected, Enter opens the checklist whose selection
	// field the cursor is in, and nothing with the cursor elsewhere.
	r.keyAt(go3270.AIDEnter, clFirstRow+1, 5, nil)
	if r.c.open != 0 {
		t.Errorf("Enter with the cursor on a name opened %d", r.c.open)
	}
	r.keyAt(go3270.AIDEnter, clFirstRow+1, 1, nil)
	if r.c.open != 2 {
		t.Errorf("Enter with the cursor in Evening's selection field opened %d, want 2", r.c.open)
	}
	r.key(go3270.AIDPF3, 0, nil)
	// A selection wins over where the cursor is.
	r.keyAt(go3270.AIDEnter, clFirstRow+1, 1, map[string]string{"sel:1": "S"})
	if r.c.open != 1 {
		t.Errorf("Enter with Morning selected, cursor on Evening, opened %d, want 1", r.c.open)
	}
	r.key(go3270.AIDPF3, 0, nil)
	// With one selected, whatever the character and wherever the cursor,
	// Enter opens it.
	r.key(go3270.AIDEnter, 21, map[string]string{"sel:2": "x"})
	if r.c.open != 2 || !strings.HasPrefix(r.rows[clHeaderRow], " Evening 0 of 0 done") {
		t.Errorf("Enter with Evening selected: open %d, header %q", r.c.open, r.rows[clHeaderRow])
	}
	if r.key(go3270.AIDPF3, 0, nil) || r.c.open != 0 {
		t.Errorf("PF3 from a checklist did not go back to the list")
	}
	// Two selected is refused, and they stay selected to fix.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S", "sel:2": "S"})
	if r.c.open != 0 || !strings.Contains(r.rows[22], "Select one checklist at a time.") ||
		!strings.HasPrefix(r.rows[clFirstRow], " S Morning") || !strings.HasPrefix(r.rows[clFirstRow+1], " S Evening") {
		t.Errorf("two selected: open %d, message %q, rows %q, %q", r.c.open, r.rows[22], r.rows[clFirstRow], r.rows[clFirstRow+1])
	}

	// PF4 changes the checklist selected, or else the one under the
	// cursor, on the bottom row.
	r.key(go3270.AIDPF4, clFirstRow, map[string]string{"sel:1": "", "sel:2": "S"})
	if r.c.editing != 2 || r.rows[21] != " Change name ===> Evening" || r.crow != 21 {
		t.Fatalf("PF4 with Evening selected: editing %d, row 21 %q, cursor row %d", r.c.editing, r.rows[21], r.crow)
	}
	r.key(go3270.AIDPF3, 0, map[string]string{"name:2": "nope"})
	if r.c.editing != 0 || r.lists()[1].Name != "Evening" {
		t.Errorf("PF3 while renaming: editing %d, name %q; want cancelled", r.c.editing, r.lists()[1].Name)
	}
	r.key(go3270.AIDPF4, clFirstRow, nil)
	r.key(go3270.AIDEnter, 0, map[string]string{"name:1": "Mornings"})
	if r.c.open != 0 || r.lists()[0].Name != "Mornings" {
		t.Errorf("rename: open %d, lists %+v", r.c.open, r.lists())
	}
	r.key(go3270.AIDPF4, 21, nil)
	if !strings.Contains(r.rows[22], "Type S beside a checklist, or put the cursor on one, then press PF4.") {
		t.Errorf("PF4 off the list: message %q", r.rows[22])
	}

	// PF10 and PF11 move the checklist selected or under the cursor, with
	// the cursor following it.
	r.key(go3270.AIDPF11, clFirstRow, nil)
	if l := r.lists(); l[0].Name != "Evening" || l[1].Name != "Mornings" || r.crow != clFirstRow+1 {
		t.Errorf("PF11: %q, %q, cursor row %d", l[0].Name, l[1].Name, r.crow)
	}
	r.key(go3270.AIDPF10, 0, map[string]string{"sel:1": "S"})
	if l := r.lists(); l[0].Name != "Mornings" || r.crow != clFirstRow || !strings.HasPrefix(r.rows[clFirstRow], "   Mornings ") || !strings.HasSuffix(r.rows[clFirstRow], " 0/0") {
		t.Errorf("PF10 with Mornings selected: first %q, cursor row %d, row %q", l[0].Name, r.crow, r.rows[clFirstRow])
	}
	r.key(go3270.AIDPF10, clFirstRow, nil)
	if !strings.Contains(r.rows[22], "That checklist is already at the top.") {
		t.Errorf("PF10 at the top: message %q", r.rows[22])
	}

	// Blanking a name removes the checklist.
	r.key(go3270.AIDPF4, clFirstRow, nil)
	r.key(go3270.AIDEnter, 0, map[string]string{"name:1": ""})
	if l := r.lists(); len(l) != 1 || l[0].Name != "Evening" || !strings.Contains(r.rows[22], "Removed 1 checklist.") {
		t.Errorf("remove: lists %+v, message %q", l, r.rows[22])
	}

	if !r.key(go3270.AIDPF3, 0, nil) {
		t.Errorf("PF3 on the list did not leave")
	}
}

func TestChecklistItems(t *testing.T) {
	r := newChecklistRig(t)
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "Trip"})
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	if r.c.open != 1 {
		t.Fatalf("did not open the checklist")
	}
	for i := range 20 {
		r.key(go3270.AIDEnter, 21, map[string]string{clNewField: fmt.Sprint("item ", i+1)})
	}
	// Enter with nothing changed, wherever the cursor, stays on the
	// checklist.
	for _, at := range [][2]int{{clFirstRow, 1}, {clFirstRow, 5}, {21, 15}} {
		r.keyAt(go3270.AIDEnter, at[0], at[1], nil)
		if r.c.open != 1 || r.c.isError {
			t.Fatalf("Enter at row %d col %d: open %d, message %q; want the checklist still shown", at[0], at[1], r.c.open, r.rows[22])
		}
	}

	// 16 items a page: the one just added is shown, on the second page.
	if r.c.itemPage != 1 || !strings.Contains(r.rows[clHeaderRow], "0 of 20 done, page 2/2") || r.rows[clFirstRow+3] != "   item 20" {
		t.Errorf("after adding 20: page %d, header %q, row %q", r.c.itemPage, r.rows[clHeaderRow], r.rows[clFirstRow+3])
	}
	r.key(go3270.AIDPF7, 0, nil)
	if r.rows[clColumnRow] != " S Item" || r.rows[clFirstRow] != "   item 1" || r.crow != clFirstRow {
		t.Errorf("first page: headings %q, first row %q, cursor row %d", r.rows[clColumnRow], r.rows[clFirstRow], r.crow)
	}
	for _, f := range r.screen {
		if f.Row >= clFirstRow && f.Row < clFirstRow+16 && f.Col == clItemCol && !f.Autoskip {
			t.Errorf("item after the done field on row %d does not skip on: %+v", f.Row, f)
		}
	}

	ids := map[string]int{}
	for _, it := range r.lists()[0].Items {
		ids[it.Text] = it.ID
	}
	done := func(text string) string { return fmt.Sprint(clDoneField, ids[text]) }
	item := func(text string) string { return fmt.Sprint(clItemField, ids[text]) }

	// Items are protected, so that tabbing goes down the done fields.
	for _, f := range r.screen {
		if f.Write && strings.HasPrefix(f.Name, clItemField) {
			t.Errorf("item field %q can be typed in", f.Name)
		}
	}

	// PF4 puts the item under the cursor on the bottom row to change, while
	// the marks typed are saved.
	r.key(go3270.AIDPF4, clFirstRow+3, map[string]string{done("item 1"): "x", done("item 2"): "X"})
	if r.c.editing != ids["item 4"] || r.crow != 21 || r.rows[21] != " Change item ===> item 4" {
		t.Fatalf("PF4: editing %d, cursor row %d, row 21 %q", r.c.editing, r.crow, r.rows[21])
	}
	r.key(go3270.AIDEnter, 0, map[string]string{item("item 4"): "item four"})
	// Blanked, the item is removed.
	r.key(go3270.AIDPF4, clFirstRow+2, nil)
	r.key(go3270.AIDEnter, 0, map[string]string{item("item 3"): ""})
	got := r.lists()[0].Items
	if len(got) != 19 || !got[0].Done || !got[1].Done || got[2].Text != "item four" || got[2].Done {
		t.Fatalf("after marking, removing and changing: %+v", got[:4])
	}
	if r.c.editing != 0 || r.rows[clFirstRow] != " X item 1" || !strings.Contains(r.rows[22], "Removed 1 item.") ||
		!strings.HasPrefix(r.rows[21], " New item ===>") {
		t.Errorf("editing %d, first row %q, message %q, bottom row %q", r.c.editing, r.rows[clFirstRow], r.rows[22], r.rows[21])
	}

	// PF3 cancels a change, still saving the marks, and stays.
	r.key(go3270.AIDPF4, clFirstRow+2, nil)
	if r.key(go3270.AIDPF3, 0, map[string]string{item("item 4"): "nope", done("item 4"): "X"}) || r.c.open == 0 || r.c.editing != 0 {
		t.Fatalf("PF3 while changing left the checklist, or stayed changing")
	}
	if got := r.lists()[0].Items[2]; got.Text != "item four" || !got.Done {
		t.Errorf("after cancelling: %+v; want the text kept and the mark saved", got)
	}
	r.key(go3270.AIDEnter, 0, map[string]string{done("item 4"): ""})

	// PF4 off the items does nothing but say so.
	r.key(go3270.AIDPF4, 21, nil)
	if r.c.editing != 0 || !strings.Contains(r.rows[22], "Put the cursor on an item") {
		t.Errorf("PF4 off the items: editing %d, message %q", r.c.editing, r.rows[22])
	}

	// A bad mark saves nothing and is left to fix, with the rest typed.
	r.key(go3270.AIDEnter, 0, map[string]string{done("item 1"): "", done("item 5"): "y"})
	if !r.c.isError || !strings.Contains(r.rows[22], `not "y"`) || !r.lists()[0].Items[0].Done {
		t.Errorf("bad mark: message %q, items %+v", r.rows[22], r.lists()[0].Items[:1])
	}
	if !strings.HasPrefix(r.rows[clFirstRow], "   item 1") || !strings.HasPrefix(r.rows[clFirstRow+3], " y item 5") {
		t.Errorf("bad mark: rows %q, %q; want what was typed kept", r.rows[clFirstRow], r.rows[clFirstRow+3])
	}
	// Fixed, it saves; unchecking item 1 is still there to save.
	r.key(go3270.AIDEnter, 0, map[string]string{done("item 1"): "", done("item 5"): "X"})
	if got := r.lists()[0].Items; got[0].Done || !got[3].Done || r.c.isError {
		t.Errorf("after fixing: %+v", got[:4])
	}

	// PF6 saves what was typed, then asks before unchecking everything.
	r.key(go3270.AIDPF4, clFirstRow+4, nil)
	r.key(go3270.AIDPF6, 0, map[string]string{item("item 6"): "item six", done("item 1"): "X"})
	text := strings.Join(r.rows, "\n")
	if !r.c.confirmReset || r.lists()[0].Items[4].Text != "item six" || !r.lists()[0].Items[0].Done {
		t.Fatalf("PF6: confirming %v, items %+v; want the change and mark saved, and a confirmation", r.c.confirmReset, r.lists()[0].Items[:5])
	}
	for _, want := range []string{"Uncheck every item of Trip?", "3 items of 19 checked:", "  item 1", "  item 2", "  item 5", "PF4=Uncheck all"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	// Only PF4 unchecks; PF3 goes back with nothing unchecked.
	r.key(go3270.AIDEnter, 0, nil)
	if !r.c.confirmReset || !r.lists()[0].Items[0].Done {
		t.Errorf("Enter on the confirmation unchecked or left it")
	}
	r.key(go3270.AIDPF3, 0, nil)
	if r.c.confirmReset || r.c.open == 0 || !r.lists()[0].Items[0].Done {
		t.Errorf("PF3 on the confirmation: confirming %v, open %d, first item %+v", r.c.confirmReset, r.c.open, r.lists()[0].Items[0])
	}
	r.key(go3270.AIDPF6, 0, nil)
	r.key(go3270.AIDPF4, 0, nil)
	for _, it := range r.lists()[0].Items {
		if it.Done {
			t.Errorf("after reset, %q is still done", it.Text)
		}
	}
	if r.c.confirmReset || !strings.Contains(r.rows[22], "Every item is unchecked.") {
		t.Errorf("after PF4: confirming %v, message %q", r.c.confirmReset, r.rows[22])
	}
	// With nothing checked there is nothing to confirm.
	r.key(go3270.AIDPF6, 0, nil)
	if r.c.confirmReset || !strings.Contains(r.rows[22], "Nothing is checked.") {
		t.Errorf("PF6 with nothing checked: confirming %v, message %q", r.c.confirmReset, r.rows[22])
	}

	// PF10 and PF11 move the item under the cursor, which the cursor
	// follows, across pages too.
	r.key(go3270.AIDPF11, clFirstRow, nil)
	if got := r.lists()[0].Items; got[0].Text != "item 2" || got[1].Text != "item 1" || r.crow != clFirstRow+1 {
		t.Errorf("PF11: %q, %q, cursor row %d", got[0].Text, got[1].Text, r.crow)
	}
	r.key(go3270.AIDPF10, clFirstRow+1, nil)
	r.key(go3270.AIDPF10, clFirstRow, nil)
	if got := r.lists()[0].Items; got[0].Text != "item 1" || !strings.Contains(r.rows[22], "already at the top") {
		t.Errorf("PF10 twice: first %q, message %q", got[0].Text, r.rows[22])
	}
	last := clFirstRow + checklistRows(24) - 1
	r.key(go3270.AIDPF11, last, nil)
	if r.c.itemPage != 1 || r.crow != clFirstRow {
		t.Errorf("moved off the page: page %d, cursor row %d; want the next page, on its first row", r.c.itemPage, r.crow)
	}
	r.key(go3270.AIDPF10, 21, nil)
	if !strings.Contains(r.rows[22], "Put the cursor on an item") {
		t.Errorf("PF10 off the items: message %q", r.rows[22])
	}

	// Another session removes the checklist: the next key goes to the list.
	if err := r.store.Update(func(l *[]checklist.Checklist, _ func() int) error { *l = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	r.key(go3270.AIDEnter, 0, map[string]string{done("item 1"): "X"})
	if r.c.open != 0 || !strings.Contains(r.rows[22], "has been removed") {
		t.Errorf("removed elsewhere: open %d, message %q", r.c.open, r.rows[22])
	}
}

// TestChecklistLongText checks that text cut to fit a field is not saved
// cut unless it is typed over.
func TestChecklistLongText(t *testing.T) {
	r := newChecklistRig(t)
	long := strings.Repeat("abcdefghij", 12)
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: long})
	r.key(go3270.AIDPF4, clFirstRow, nil)
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: long})
	r.key(go3270.AIDPF4, clFirstRow, nil)
	r.key(go3270.AIDEnter, 0, map[string]string{"done:2": "X"})
	l := r.lists()[0]
	if l.Name != long || l.Items[0].Text != long || !l.Items[0].Done {
		t.Errorf("long text was cut: %+v", l)
	}
}

// TestChecklistSessionsMerge checks that a session saves only what was typed
// on it: a change another session made since it drew its screen stays.
func TestChecklistSessionsMerge(t *testing.T) {
	a := newChecklistRig(t)
	a.key(go3270.AIDEnter, 21, map[string]string{clNewField: "Trip"})
	a.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	a.key(go3270.AIDEnter, 21, map[string]string{clNewField: "passport"})
	a.key(go3270.AIDEnter, 21, map[string]string{clNewField: "charger"})

	b := newChecklistRigOn(t, a.store)
	b.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})

	// Both screens show nothing checked. A checks the passport, then B,
	// its screen not yet showing that, checks the charger.
	a.key(go3270.AIDEnter, 0, map[string]string{"done:2": "X"})
	b.key(go3270.AIDEnter, 0, map[string]string{"done:3": "X"})
	for _, it := range a.lists()[0].Items {
		if !it.Done {
			t.Errorf("%q is not done; want both sessions' checks kept", it.Text)
		}
	}
	if b.rows[clFirstRow] != " X passport" || b.rows[clFirstRow+1] != " X charger" {
		t.Errorf("B shows %q, %q; want both checked", b.rows[clFirstRow], b.rows[clFirstRow+1])
	}
	// A, still showing the charger unchecked, presses Enter: the charger
	// stays checked, and A now shows it.
	a.key(go3270.AIDEnter, 0, nil)
	if !a.lists()[0].Items[1].Done || a.rows[clFirstRow+1] != " X charger" {
		t.Errorf("after A's Enter: charger %+v, row %q", a.lists()[0].Items[1], a.rows[clFirstRow+1])
	}
}

func TestChecklistItemColors(t *testing.T) {
	r := newChecklistRig(t)
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "Trip"})
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "passport"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "charger"})
	r.key(go3270.AIDEnter, 0, map[string]string{"done:2": "X"})
	colors := map[string]go3270.Color{}
	for _, f := range r.screen {
		if f.Row >= clFirstRow && f.Col == clItemCol {
			colors[f.Content] = f.Color
		}
	}
	if colors["passport"] != go3270.Green || colors["charger"] != go3270.Red {
		t.Errorf("colors %v; want done green and not done red", colors)
	}
}

func TestChecklistViewers(t *testing.T) {
	v := NewViewers()
	if n := v.Set(1, 7); n != 0 {
		t.Errorf("first viewer sees %d others", n)
	}
	v.Set(2, 7)
	v.Set(3, 8)
	if n := v.Set(1, 7); n != 1 {
		t.Errorf("session 1 sees %d others on checklist 7, want 1", n)
	}
	v.Set(2, 0) // left the checklist
	if n := v.Set(1, 7); n != 0 {
		t.Errorf("after session 2 left, session 1 sees %d others", n)
	}
	if n := (*Viewers)(nil).Set(1, 7); n != 0 {
		t.Errorf("nil Viewers counted %d", n)
	}

	r := newChecklistRig(t)
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "Trip"})
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	if strings.Contains(r.rows[clHeaderRow], "viewing") {
		t.Errorf("alone, header is %q", r.rows[clHeaderRow])
	}
	for n, want := range map[int]string{1: "(1 other session viewing)", 2: "(2 other sessions viewing)"} {
		r.c.others = n
		r.draw()
		if !strings.Contains(r.rows[clHeaderRow], want) {
			t.Errorf("%d others: header %q, want %q", n, r.rows[clHeaderRow], want)
		}
	}
}

func TestChecklistStars(t *testing.T) {
	r := newChecklistRig(t)
	for _, name := range []string{"A", "B", "C", "D"} {
		r.key(go3270.AIDEnter, 21, map[string]string{clNewField: name})
	}
	// Give B an item not done, so its count is red; A, C and D have none.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:2": "S"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField: "thing"})
	r.key(go3270.AIDPF3, 0, nil)

	names := func() []string {
		var out []string
		for row := clFirstRow; row < clFirstRow+4; row++ {
			words := strings.Fields(r.rows[row])
			if words[0] == "*" {
				words = append([]string{"*" + words[1]}, words[2:]...)
			}
			out = append(out, words[0])
		}
		return out
	}
	field := func(row, col int) go3270.Field {
		for _, f := range r.screen {
			if f.Row == row && f.Col == col {
				return f
			}
		}
		t.Fatalf("no field at row %d col %d", row, col)
		return go3270.Field{}
	}

	if got := fmt.Sprint(names()); got != "[A B C D]" {
		t.Fatalf("order %s", got)
	}
	if field(clFirstRow+1, 80-clCountWidth).Color != go3270.Red || field(clFirstRow, 80-clCountWidth).Color != go3270.Green {
		t.Errorf("counts: B's %v, A's %v; want red for items not done, green for all done", field(clFirstRow+1, 80-clCountWidth).Color, field(clFirstRow, 80-clCountWidth).Color)
	}

	// Starring C and B moves them to the top, in their own order, with the
	// star shown and the name red or green by whether everything is done.
	// Typing a star, with the cursor in that column, does not open it.
	r.keyAt(go3270.AIDEnter, clFirstRow+2, 1, map[string]string{"sel:3": "*", "sel:2": "*"})
	if r.c.open != 0 {
		t.Fatalf("starring opened %d", r.c.open)
	}
	if got := fmt.Sprint(names()); got != "[*B *C A D]" {
		t.Errorf("after starring: order %s", got)
	}
	if !r.lists()[1].Active || r.lists()[1].Name != "B" {
		t.Errorf("stars are not saved in place: %+v", r.lists())
	}
	if f := field(clFirstRow, clItemCol); f.Content != "B" || f.Color != go3270.Red {
		t.Errorf("starred B, not done, is %+v; want red", f)
	}
	if f := field(clFirstRow+1, clItemCol); f.Content != "C" || f.Color != go3270.Green {
		t.Errorf("starred C, all done, is %+v; want green", f)
	}
	if f := field(clFirstRow+2, clItemCol); f.Color != go3270.Turquoise {
		t.Errorf("unstarred A is %v; want turquoise", f.Color)
	}

	// A starred checklist is opened by selecting it over its star, which
	// stays, or by Enter in its column.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:3": "S"})
	if r.c.open != 3 {
		t.Errorf("S over C's star opened %d", r.c.open)
	}
	r.key(go3270.AIDPF3, 0, nil)
	if !r.lists()[2].Active {
		t.Errorf("selecting C unstarred it")
	}
	r.keyAt(go3270.AIDEnter, clFirstRow, 1, nil)
	if r.c.open != 2 {
		t.Errorf("Enter in B's column opened %d", r.c.open)
	}
	r.key(go3270.AIDPF3, 0, nil)

	// Starred checklists cannot be moved; an unstarred one moves past them.
	r.key(go3270.AIDPF11, clFirstRow, nil)
	if !strings.Contains(r.rows[22], "Starred checklists stay at the top") || fmt.Sprint(names()) != "[*B *C A D]" {
		t.Errorf("moving a starred checklist: message %q, order %v", r.rows[22], names())
	}
	r.key(go3270.AIDPF10, clFirstRow+2, nil)
	if !strings.Contains(r.rows[22], "already at the top") {
		t.Errorf("moving A up past the starred: message %q", r.rows[22])
	}
	r.key(go3270.AIDPF11, clFirstRow+2, nil)
	if got := fmt.Sprint(names()); got != "[*B *C D A]" || r.crow != clFirstRow+3 {
		t.Errorf("A moved down: order %s, cursor row %d", got, r.crow)
	}
	r.key(go3270.AIDPF10, clFirstRow+3, nil)

	// Unstarring puts a checklist back where it was.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:3": ""})
	if got := fmt.Sprint(names()); got != "[*B A C D]" {
		t.Errorf("after unstarring C: order %s", got)
	}
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:2": ""})
	if got := fmt.Sprint(names()); got != "[A B C D]" {
		t.Errorf("after unstarring B: order %s", got)
	}
}
