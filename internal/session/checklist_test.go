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
	if !strings.HasPrefix(r.rows[clHeaderRow], " CHECKLISTS 0 checklists") || !strings.HasPrefix(r.rows[clColumnRow], " S * Name") {
		t.Errorf("empty list: header %q, headings %q", r.rows[clHeaderRow], r.rows[clColumnRow])
	}
	if r.crow != clFirstRow || r.rows[clFirstRow] != "" || r.rows[21] != "" {
		t.Errorf("empty list: cursor on row %d, rows %q, %q; want the cursor on the blank first row, and no row for adding below",
			r.crow, r.rows[clFirstRow], r.rows[21])
	}

	// A name typed on the blank row is added, with the blank row after it
	// and the cursor there to add another.
	r.key(go3270.AIDEnter, clFirstRow, map[string]string{clNewField + "0": "Morning"})
	r.key(go3270.AIDEnter, clFirstRow+1, map[string]string{clNewField + "0": "Evening"})
	if l := r.lists(); len(l) != 2 || l[0].Name != "Morning" || l[1].Name != "Evening" {
		t.Fatalf("after adding: %+v", l)
	}
	if r.crow != clFirstRow+2 || r.rows[clFirstRow+2] != "" {
		t.Errorf("after adding, cursor on row %d, row %q; want on the blank row after Evening", r.crow, r.rows[clFirstRow+2])
	}
	if !strings.HasPrefix(r.rows[clColumnRow], " S * Name") || !strings.HasSuffix(r.rows[clColumnRow], "Done") {
		t.Errorf("headings %q", r.rows[clColumnRow])
	}
	if !strings.HasPrefix(r.rows[clFirstRow], "     Morning") || !strings.HasSuffix(r.rows[clFirstRow], " 0/0") {
		t.Errorf("first row %q", r.rows[clFirstRow])
	}
	// Names can be typed over.
	names := 0
	for _, f := range r.screen {
		if f.Write && strings.HasPrefix(f.Name, clNameField) {
			names++
		}
	}
	if names != 2 {
		t.Errorf("%d name fields can be typed in, want 2", names)
	}

	// With nothing selected, Enter opens nothing, wherever the cursor.
	for _, col := range []int{1, 5} {
		r.keyAt(go3270.AIDEnter, clFirstRow+1, col, nil)
		if r.c.open != 0 {
			t.Errorf("Enter with nothing selected, cursor in column %d of Evening, opened %d", col, r.c.open)
		}
	}
	// A selection opens the checklist, wherever the cursor is.
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
		!strings.HasPrefix(r.rows[clFirstRow], " S   Morning") || !strings.HasPrefix(r.rows[clFirstRow+1], " S   Evening") {
		t.Errorf("two selected: open %d, message %q, rows %q, %q", r.c.open, r.rows[22], r.rows[clFirstRow], r.rows[clFirstRow+1])
	}

	// PF4 on the list only says how to change a name.
	r.key(go3270.AIDPF4, clFirstRow, map[string]string{"sel:1": "", "sel:2": ""})
	if !strings.Contains(r.rows[22], "Type over a name") {
		t.Errorf("PF4 on the list: message %q", r.rows[22])
	}

	// A rename is confirmed before it is saved; Enter is not confirmation.
	r.key(go3270.AIDEnter, 0, map[string]string{"name:1": "Mornings"})
	text := strings.Join(r.rows, "\n")
	if r.c.held == nil || r.lists()[0].Name != "Morning" {
		t.Fatalf("rename: confirming %v, lists %+v; want asked, not saved", r.c.held != nil, r.lists())
	}
	for _, want := range []string{"Rename 1 checklist?", "Rename Morning to Mornings", "PF4=Save"} {
		if !strings.Contains(text, want) {
			t.Errorf("rename confirmation lacks %q:\n%s", want, text)
		}
	}
	r.key(go3270.AIDEnter, 0, nil)
	if r.c.held == nil || r.lists()[0].Name != "Morning" {
		t.Errorf("Enter on the confirmation saved or left it")
	}
	// PF3 goes back with the name still typed; Enter asks again.
	r.key(go3270.AIDPF3, 0, nil)
	if r.c.held != nil || r.lists()[0].Name != "Morning" || !strings.HasPrefix(r.rows[clFirstRow], "     Mornings") {
		t.Errorf("PF3 on the confirmation: confirming %v, saved %q, row %q; want back with the name typed",
			r.c.held != nil, r.lists()[0].Name, r.rows[clFirstRow])
	}
	r.key(go3270.AIDEnter, 0, nil)
	if r.c.held == nil {
		t.Fatalf("Enter with the rename still typed did not ask again")
	}
	r.key(go3270.AIDPF4, 0, nil)
	if r.c.held != nil || r.lists()[0].Name != "Mornings" || !strings.Contains(r.rows[22], "Renamed 1 checklist.") {
		t.Errorf("PF4 on the confirmation: lists %+v, message %q", r.lists(), r.rows[22])
	}
	// PF12 discards everything typed with it, star and all.
	r.key(go3270.AIDEnter, 0, map[string]string{"name:1": "Nope", "star:2": "*"})
	r.key(go3270.AIDPF12, 0, nil)
	if l := r.lists(); r.c.held != nil || l[0].Name != "Mornings" || l[1].Active || !strings.HasPrefix(r.rows[clFirstRow], "     Mornings") {
		t.Errorf("PF12 on the confirmation: lists %+v, row %q", l, r.rows[clFirstRow])
	}

	// PF10 and PF11 move the checklist selected or under the cursor, with
	// the cursor following it.
	r.key(go3270.AIDPF11, clFirstRow, nil)
	if l := r.lists(); l[0].Name != "Evening" || l[1].Name != "Mornings" || r.crow != clFirstRow+1 {
		t.Errorf("PF11: %q, %q, cursor row %d", l[0].Name, l[1].Name, r.crow)
	}
	r.key(go3270.AIDPF10, 0, map[string]string{"sel:1": "S"})
	if l := r.lists(); l[0].Name != "Mornings" || r.crow != clFirstRow || !strings.HasPrefix(r.rows[clFirstRow], "     Mornings ") || !strings.HasSuffix(r.rows[clFirstRow], " 0/0") {
		t.Errorf("PF10 with Mornings selected: first %q, cursor row %d, row %q", l[0].Name, r.crow, r.rows[clFirstRow])
	}
	r.key(go3270.AIDPF10, clFirstRow, nil)
	if !strings.Contains(r.rows[22], "That checklist is already at the top.") {
		t.Errorf("PF10 at the top: message %q", r.rows[22])
	}

	// Blanking a name removes the checklist, once confirmed, with what was
	// typed alongside: a checklist added and one renamed.
	r.key(go3270.AIDEnter, 0, map[string]string{"name:1": "", "name:2": "Evenings", clNewField + "0": "Noon"})
	text = strings.Join(r.rows, "\n")
	for _, want := range []string{"Rename 1 checklist and remove 1 checklist?", "Remove Mornings (0 items, 0 done)", "Rename Evening to Evenings"} {
		if !strings.Contains(text, want) {
			t.Errorf("remove confirmation lacks %q:\n%s", want, text)
		}
	}
	r.key(go3270.AIDPF4, 0, nil)
	if l := r.lists(); len(l) != 2 || l[0].Name != "Evenings" || l[1].Name != "Noon" ||
		!strings.Contains(r.rows[22], "Renamed 1 checklist. Removed 1 checklist.") {
		t.Errorf("remove: lists %+v, message %q", l, r.rows[22])
	}

	// Enter on a name typed over, with nothing selected, asks first, then
	// saves it and stays on the list.
	r.keyAt(go3270.AIDEnter, clFirstRow, 5, map[string]string{"name:2": "Evenings!"})
	if r.c.held == nil || r.c.open != 0 {
		t.Fatalf("Enter on a renamed name: confirming %v, open %d; want asked first", r.c.held != nil, r.c.open)
	}
	r.key(go3270.AIDPF4, 0, nil)
	if r.c.open != 0 || r.lists()[0].Name != "Evenings!" {
		t.Errorf("PF4 after Enter on a name: open %d, lists %+v", r.c.open, r.lists())
	}
	// With it selected too, PF4 saves it, then opens it.
	r.keyAt(go3270.AIDEnter, clFirstRow, 5, map[string]string{"name:2": "Evenings", "sel:2": "X"})
	r.key(go3270.AIDPF4, 0, nil)
	if r.c.open != 2 || r.lists()[0].Name != "Evenings" || !strings.HasPrefix(r.rows[clHeaderRow], " Evenings ") {
		t.Errorf("PF4 after Enter with a renamed checklist selected: open %d, lists %+v, header %q", r.c.open, r.lists(), r.rows[clHeaderRow])
	}
	r.key(go3270.AIDPF3, 0, nil)
	// PF12 discards the rename, and opens nothing.
	r.keyAt(go3270.AIDEnter, clFirstRow, 5, map[string]string{"name:2": "nope", "sel:2": "X"})
	r.key(go3270.AIDPF12, 0, nil)
	if r.c.open != 0 || r.lists()[0].Name != "Evenings" {
		t.Errorf("PF12 after Enter on a name: open %d, lists %+v", r.c.open, r.lists())
	}
	// A checklist selected with its name blanked is removed once
	// confirmed, and not opened.
	r.keyAt(go3270.AIDEnter, clFirstRow+1, 5, map[string]string{"name:3": "", "sel:3": "X"})
	r.key(go3270.AIDPF4, 0, nil)
	if l := r.lists(); r.c.open != 0 || len(l) != 1 || r.c.isError {
		t.Errorf("Enter with a blanked name selected, confirmed: open %d, lists %+v, message %q", r.c.open, l, r.rows[22])
	}

	// PF3 with nothing typed but a selection goes straight back.
	if !r.key(go3270.AIDPF3, 0, map[string]string{"sel:2": "S"}) || r.c.held != nil {
		t.Errorf("PF3 with only a selection typed did not leave")
	}
}

// TestChecklistAddSeveral checks that the last page is filled out with
// blank entries, and that several typed at once are added in the order
// shown, skipping those left blank.
func TestChecklistAddSeveral(t *testing.T) {
	r := newChecklistRig(t)
	perPage := checklistRows(24)
	blanks := func() int {
		n := 0
		for _, f := range r.screen {
			if strings.HasPrefix(f.Name, clNewField) {
				n++
			}
		}
		return n
	}
	if n := blanks(); n != perPage {
		t.Errorf("empty list has %d blank entries, want %d", n, perPage)
	}

	r.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": "A", clNewField + "2": "C", clNewField + "3": "D"})
	if got := fmt.Sprint(names(r.lists())); got != "[A C D]" {
		t.Errorf("added %s, want [A C D]", got)
	}
	if n := blanks(); n != perPage-3 || r.crow != clFirstRow+3 {
		t.Errorf("after adding 3: %d blank entries, cursor on row %d; want %d, on the first blank", n, r.crow, perPage-3)
	}

	// Each blank checklist has a star field.
	stars := 0
	for _, f := range r.screen {
		if strings.HasPrefix(f.Name, clNewStar) {
			stars++
			if !f.Write || f.Col != clStarCol {
				t.Errorf("blank checklist's star field is %+v", f)
			}
		}
	}
	if stars != perPage-3 {
		t.Errorf("%d star fields on blank checklists, want %d", stars, perPage-3)
	}

	// PF3 lists each one to add, and which are starred.
	typed := map[string]string{clNewField + "0": "E", clNewStar + "0": "*", clNewField + "1": "F", clNewStar + "2": "*"}
	r.key(go3270.AIDPF3, 0, typed)
	if text := strings.Join(r.rows, "\n"); !strings.Contains(text, "Add E (starred)") || !strings.Contains(text, "Add F\n") {
		t.Errorf("confirmation lacks the adds:\n%s", text)
	}
	// Saved, E is starred, F is not, and the star beside no name adds
	// nothing.
	r.key(go3270.AIDPF4, 0, nil)
	if l := r.lists(); len(l) != 5 || l[3].Name != "E" || !l[3].Active || l[4].Name != "F" || l[4].Active {
		t.Errorf("after adding starred: %+v", l)
	}
	r = newChecklistRigOn(t, r.store)

	// On a checklist too, without stars.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	if n := blanks(); n != perPage {
		t.Errorf("empty checklist has %d blank entries, want %d", n, perPage)
	}
	for _, f := range r.screen {
		if strings.HasPrefix(f.Name, clNewStar) {
			t.Errorf("blank item has a star field: %+v", f)
		}
	}
	r.key(go3270.AIDEnter, 0, map[string]string{clNewField + "1": "socks", clNewField + "0": "passport"})
	var items []string
	for _, it := range r.lists()[0].Items {
		items = append(items, it.Text)
	}
	if fmt.Sprint(items) != "[passport socks]" || r.crow != clFirstRow+2 {
		t.Errorf("items %v, cursor on row %d; want [passport socks], on the first blank", items, r.crow)
	}
}

// names are the names of lists, in order.
func names(lists []checklist.Checklist) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l.Name)
	}
	return out
}

// TestChecklistBackUnsaved checks that PF3 with anything typed asks whether
// to save it before going back.
func TestChecklistBackUnsaved(t *testing.T) {
	r := newChecklistRig(t)
	r.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": "Trip"})
	r.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": "Gym"})

	// On the list: a star, a rename and a checklist to add.
	typed := map[string]string{"star:1": "*", "name:2": "Gym bag", clNewField + "0": "Work"}
	if r.key(go3270.AIDPF3, 0, typed) || r.c.held == nil {
		t.Fatalf("PF3 with changes typed left, or did not ask")
	}
	text := strings.Join(r.rows, "\n")
	for _, want := range []string{"SAVE CHANGES", "Save these changes before going back?", "Star Trip", "Rename Gym to Gym bag", "Add Work", "PF12=Discard"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	// PF3 there returns to the list with it all still typed.
	if r.key(go3270.AIDPF3, 0, nil) || r.c.held != nil || !strings.HasPrefix(r.rows[clFirstRow], "   * Trip") ||
		!strings.HasPrefix(r.rows[clFirstRow+1], "     Gym bag") || !strings.HasPrefix(r.rows[clFirstRow+2], "     Work") {
		t.Errorf("PF3 on the confirmation: rows %q, %q, %q", r.rows[clFirstRow], r.rows[clFirstRow+1], r.rows[clFirstRow+2])
	}
	if l := r.lists(); len(l) != 2 || l[0].Active || l[1].Name != "Gym" {
		t.Errorf("PF3 on the confirmation saved: %+v", l)
	}
	// PF12 discards it and goes back.
	r.key(go3270.AIDPF3, 0, nil)
	if !r.key(go3270.AIDPF12, 0, nil) {
		t.Errorf("PF12 on the confirmation did not go back")
	}
	if l := r.lists(); len(l) != 2 || l[0].Active || l[1].Name != "Gym" {
		t.Errorf("PF12 saved: %+v", l)
	}
	// PF4 saves it and goes back.
	r = newChecklistRigOn(t, r.store)
	r.key(go3270.AIDPF3, 0, typed)
	if !r.key(go3270.AIDPF4, 0, nil) {
		t.Errorf("PF4 on the confirmation did not go back")
	}
	if l := r.lists(); len(l) != 3 || !l[0].Active || l[1].Name != "Gym bag" || l[2].Name != "Work" {
		t.Errorf("PF4 did not save it all: %+v", l)
	}

	// On a checklist, a mark and an item typed; PF12 goes back to the list
	// without them.
	r = newChecklistRigOn(t, r.store)
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	r.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": "passport"})
	id := r.lists()[0].Items[0].ID
	typed = map[string]string{fmt.Sprint(clDoneField, id): "X", clNewField + "0": "charger"}
	r.key(go3270.AIDPF3, 0, typed)
	text = strings.Join(r.rows, "\n")
	if r.c.held == nil || !strings.Contains(text, "Check passport") || !strings.Contains(text, "Add charger") {
		t.Fatalf("PF3 on a checklist with changes typed:\n%s", text)
	}
	if r.key(go3270.AIDPF12, 0, nil) || r.c.open != 0 || len(r.lists()[0].Items) != 1 || r.lists()[0].Items[0].Done {
		t.Errorf("PF12: open %d, items %+v; want back on the list, nothing saved", r.c.open, r.lists()[0].Items)
	}
	// PF4 saves them and goes back to the list.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	r.key(go3270.AIDPF3, 0, typed)
	if r.key(go3270.AIDPF4, 0, nil) || r.c.open != 0 || len(r.lists()[0].Items) != 2 || !r.lists()[0].Items[0].Done {
		t.Errorf("PF4: open %d, items %+v; want back on the list, both saved", r.c.open, r.lists()[0].Items)
	}
	// A bad mark is shown; PF4 then leaves it on the checklist to fix.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	r.key(go3270.AIDPF3, 0, map[string]string{fmt.Sprint(clDoneField, id): "y"})
	if text := strings.Join(r.rows, "\n"); !strings.Contains(text, `not "y"`) {
		t.Errorf("bad mark not shown on the confirmation:\n%s", text)
	}
	r.key(go3270.AIDPF4, 0, nil)
	if r.c.open != 1 || !r.c.isError || !strings.HasPrefix(r.rows[clFirstRow], " y passport") {
		t.Errorf("PF4 with a bad mark: open %d, message %q, row %q", r.c.open, r.rows[22], r.rows[clFirstRow])
	}
	// Put back as saved, it is nothing typed: PF3 goes straight back.
	r.key(go3270.AIDPF3, 0, map[string]string{fmt.Sprint(clDoneField, id): "X"})
	if r.c.open != 0 || r.c.held != nil {
		t.Errorf("PF3 with nothing typed: open %d, asking %v", r.c.open, r.c.held != nil)
	}
}

// TestChecklistListPages checks that the blank row for adding follows the
// last checklist, on a page of its own if need be.
func TestChecklistListPages(t *testing.T) {
	r := newChecklistRig(t)
	perPage := checklistRows(24)
	for i := range perPage {
		r.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": fmt.Sprint("list ", i+1)})
	}
	if r.c.listPage != 1 || r.crow != clFirstRow || r.rows[clFirstRow] != "" || !strings.Contains(r.rows[clHeaderRow], "page 2/2") {
		t.Errorf("a full page: page %d, cursor row %d, header %q; want the blank row alone on page 2", r.c.listPage, r.crow, r.rows[clHeaderRow])
	}
	r.key(go3270.AIDPF7, 0, nil)
	last := clFirstRow + perPage - 1
	if !strings.HasPrefix(r.rows[last], fmt.Sprint("     list ", perPage)) || r.rows[last+1] != "" {
		t.Errorf("page 1 ends %q, %q", r.rows[last], r.rows[last+1])
	}
	for _, f := range r.screen {
		if strings.HasPrefix(f.Name, clNewField) {
			t.Errorf("page 1 has the blank row for adding: %+v", f)
		}
	}
}

func TestChecklistItems(t *testing.T) {
	r := newChecklistRig(t)
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "Trip"})
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	if r.c.open != 1 {
		t.Fatalf("did not open the checklist")
	}
	for i := range 20 {
		r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": fmt.Sprint("item ", i+1)})
	}
	if r.crow != clFirstRow+3 || r.rows[clFirstRow+3] != "" {
		t.Errorf("after adding, cursor on row %d, row %q; want on the blank item after item 20", r.crow, r.rows[clFirstRow+3])
	}
	// Enter with nothing changed, wherever the cursor, stays on the
	// checklist.
	for _, at := range [][2]int{{clFirstRow, 1}, {clFirstRow, 5}, {21, 15}} {
		r.keyAt(go3270.AIDEnter, at[0], at[1], nil)
		if r.c.open != 1 || r.c.isError {
			t.Fatalf("Enter at row %d col %d: open %d, message %q; want the checklist still shown", at[0], at[1], r.c.open, r.rows[22])
		}
	}

	// 17 rows a page, the last ending with the blank item: the one just
	// added is shown, on the second page.
	if r.c.itemPage != 1 || !strings.Contains(r.rows[clHeaderRow], "0 of 20 done, page 2/2") || r.rows[clFirstRow+2] != "   item 20" || r.rows[21] != "" {
		t.Errorf("after adding 20: page %d, header %q, row %q, row 21 %q; want no row for adding at the bottom",
			r.c.itemPage, r.rows[clHeaderRow], r.rows[clFirstRow+2], r.rows[21])
	}
	r.key(go3270.AIDPF7, 0, nil)
	if r.rows[clColumnRow] != " S Item" || r.rows[clFirstRow] != "   item 1" || r.crow != clFirstRow {
		t.Errorf("first page: headings %q, first row %q, cursor row %d", r.rows[clColumnRow], r.rows[clFirstRow], r.crow)
	}
	for _, f := range r.screen {
		if strings.HasPrefix(f.Name, clNewField) {
			t.Errorf("first page has the blank item for adding: %+v", f)
		}
	}

	ids := map[string]int{}
	for _, it := range r.lists()[0].Items {
		ids[it.Text] = it.ID
	}
	done := func(text string) string { return fmt.Sprint(clDoneField, ids[text]) }
	item := func(text string) string { return fmt.Sprint(clItemField, ids[text]) }

	// Items can be typed over.
	items := 0
	for _, f := range r.screen {
		if f.Write && strings.HasPrefix(f.Name, clItemField) {
			items++
		}
	}
	if items != 17 {
		t.Errorf("%d item fields can be typed in, want 17", items)
	}

	// An item typed over or blanked is confirmed before anything typed is
	// saved.
	r.key(go3270.AIDEnter, 0, map[string]string{done("item 1"): "x", done("item 2"): "X", item("item 4"): "item four", item("item 3"): ""})
	text := strings.Join(r.rows, "\n")
	if r.c.held == nil || r.lists()[0].Items[0].Done {
		t.Fatalf("change and remove: asking %v, items %+v; want asked, nothing saved", r.c.held != nil, r.lists()[0].Items[:4])
	}
	for _, want := range []string{"CHANGE ITEMS", "Change 1 item and remove 1 item?", "Check item 1", "Check item 2", "Remove item 3", "Change item 4 to item four"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	r.key(go3270.AIDPF4, 0, nil)
	got := r.lists()[0].Items
	if len(got) != 19 || !got[0].Done || !got[1].Done || got[2].Text != "item four" || got[2].Done {
		t.Fatalf("after marking, removing and changing: %+v", got[:4])
	}
	if r.c.open != 1 || r.rows[clFirstRow] != " X item 1" || !strings.Contains(r.rows[22], "Changed 1 item. Removed 1 item.") {
		t.Errorf("open %d, first row %q, message %q", r.c.open, r.rows[clFirstRow], r.rows[22])
	}
	// PF12 discards a change, and the marks typed with it.
	r.key(go3270.AIDEnter, 0, map[string]string{item("item 4"): "nope", done("item 5"): "X"})
	r.key(go3270.AIDPF12, 0, nil)
	if got := r.lists()[0].Items; got[2].Text != "item four" || got[3].Done || r.c.open != 1 {
		t.Errorf("after discarding: %+v, open %d", got[2:4], r.c.open)
	}

	// PF4 does nothing but say how to change an item.
	r.key(go3270.AIDPF4, clFirstRow, nil)
	if !strings.Contains(r.rows[22], "Type over an item") {
		t.Errorf("PF4 on the items: message %q", r.rows[22])
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
	r.key(go3270.AIDPF6, 0, map[string]string{done("item 1"): "X"})
	text = strings.Join(r.rows, "\n")
	if !r.c.confirmReset || !r.lists()[0].Items[0].Done {
		t.Fatalf("PF6: confirming %v, items %+v; want the mark saved, and a confirmation", r.c.confirmReset, r.lists()[0].Items[:5])
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
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": long})
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": long})
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
	a.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "Trip"})
	a.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	a.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "passport"})
	a.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "charger"})

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
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "Trip"})
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:1": "S"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "passport"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "charger"})
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
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "Trip"})
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
		r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": name})
	}
	// Give B an item not done, so its count is red; A, C and D have none.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:2": "S"})
	r.key(go3270.AIDEnter, 21, map[string]string{clNewField + "0": "thing"})
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
	// Any character stars; typing one does not open the checklist.
	r.keyAt(go3270.AIDEnter, clFirstRow+2, 3, map[string]string{"star:3": "*", "star:2": "x"})
	if r.c.open != 0 {
		t.Fatalf("starring opened %d", r.c.open)
	}
	if got := fmt.Sprint(names()); got != "[*B *C A D]" {
		t.Errorf("after starring: order %s", got)
	}
	if !r.lists()[1].Active || r.lists()[1].Name != "B" {
		t.Errorf("stars are not saved in place: %+v", r.lists())
	}
	// The star is in its own column, the selection column left blank.
	if !strings.HasPrefix(r.rows[clFirstRow], "   * B ") || field(clFirstRow, 0).Content != "" || field(clFirstRow, clStarCol).Content != "*" {
		t.Errorf("starred B's row is %q; want the star in column 3 and nothing to select", r.rows[clFirstRow])
	}
	if f := field(clFirstRow, clNameCol); f.Content != "B" || f.Color != go3270.Red {
		t.Errorf("starred B, not done, is %+v; want red", f)
	}
	if f := field(clFirstRow+1, clNameCol); f.Content != "C" || f.Color != go3270.Green {
		t.Errorf("starred C, all done, is %+v; want green", f)
	}
	if f := field(clFirstRow+2, clNameCol); f.Color != go3270.Turquoise {
		t.Errorf("unstarred A is %v; want turquoise", f.Color)
	}

	// A starred checklist is opened by selecting it, which leaves its star,
	// and not by Enter in its column with nothing typed.
	r.key(go3270.AIDEnter, 0, map[string]string{"sel:3": "S"})
	if r.c.open != 3 {
		t.Errorf("S beside C's star opened %d", r.c.open)
	}
	r.key(go3270.AIDPF3, 0, nil)
	if !r.lists()[2].Active {
		t.Errorf("selecting C unstarred it")
	}
	r.keyAt(go3270.AIDEnter, clFirstRow, 1, nil)
	if r.c.open != 0 {
		t.Errorf("Enter in B's column opened %d", r.c.open)
	}

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
	r.key(go3270.AIDEnter, 0, map[string]string{"star:3": ""})
	if got := fmt.Sprint(names()); got != "[*B A C D]" {
		t.Errorf("after unstarring C: order %s", got)
	}
	r.key(go3270.AIDEnter, 0, map[string]string{"star:2": ""})
	if got := fmt.Sprint(names()); got != "[A B C D]" {
		t.Errorf("after unstarring B: order %s", got)
	}
}

// TestChecklistOwners checks that each user sees, changes and moves only
// their own checklists, in a file shared with others'.
func TestChecklistOwners(t *testing.T) {
	a := newChecklistRig(t)
	a.c.owner = 1
	a.draw()
	b := newChecklistRigOn(t, a.store)
	b.c.owner = 2
	b.draw()

	a.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": "A1"})
	b.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": "B1"})
	a.key(go3270.AIDEnter, 0, map[string]string{clNewField + "0": "A2"})
	a.draw()
	b.draw()

	if got := names(checklist.Owned(a.lists(), 1)); fmt.Sprint(got) != "[A1 A2]" {
		t.Errorf("user 1 owns %v", got)
	}
	if !strings.HasPrefix(a.rows[clHeaderRow], " CHECKLISTS 2 checklists") || strings.Contains(strings.Join(a.rows, "\n"), "B1") {
		t.Errorf("user 1 sees:\n%s", strings.Join(a.rows, "\n"))
	}
	if !strings.HasPrefix(b.rows[clHeaderRow], " CHECKLISTS 1 checklist") || strings.Contains(strings.Join(b.rows, "\n"), "A1") {
		t.Errorf("user 2 sees:\n%s", strings.Join(b.rows, "\n"))
	}

	// Moving A2 up passes over B1, which lies between them in the file.
	a.key(go3270.AIDPF10, clFirstRow+1, nil)
	if got := names(checklist.Owned(a.lists(), 1)); fmt.Sprint(got) != "[A2 A1]" || a.c.isError {
		t.Errorf("after moving up: %v, message %q", got, a.rows[22])
	}
	a.key(go3270.AIDPF10, clFirstRow, nil)
	if !strings.Contains(a.rows[22], "already at the top") {
		t.Errorf("moving the top one up: %q", a.rows[22])
	}

	// Another user's checklist cannot be opened, even by its ID.
	b.c.open = 1
	b.draw()
	if b.c.open != 0 || !strings.Contains(b.rows[22], "has been removed") {
		t.Errorf("user 2 opened user 1's checklist: open %d", b.c.open)
	}

	// The dashboard lists only the user's own starred checklists.
	a.key(go3270.AIDEnter, 0, map[string]string{"star:1": "*"})
	b.key(go3270.AIDEnter, 0, map[string]string{"star:2": "*"})
	cfg := Config{Checklists: a.store}
	if got := names(gather(cfg, now, nil, nil, 2, nil).Checklists); fmt.Sprint(got) != "[B1]" {
		t.Errorf("user 2's dashboard checklists: %v", got)
	}
}
