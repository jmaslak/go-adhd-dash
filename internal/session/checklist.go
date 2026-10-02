package session

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/checklist"
)

// Checklist screen layout, for both the list of checklists and one
// checklist's items: the heading, the column headings, one entry per row,
// then blank entries, typed in to add more (on the list, one after the last
// checklist; on a checklist, filling out the last page), then a blank row
// and the message and help rows.
//
// On the list, each row is a one-character selection field, a one-character
// star field, the checklist's name, then how many of its items are done.
// The star field shows a * for a checklist starred as active; those are
// listed first. On a checklist, each row is a one-character done field,
// then the item. Names and items are typed over to change them, or blanked
// to remove them, which is saved only once confirmed.
const (
	clHeaderRow  = 2
	clColumnRow  = 3
	clFirstRow   = 4
	clCountWidth = 12 // the list's done count, with its attribute byte
	clItemCol    = 2  // an item's attribute byte, after its done field
	clStarCol    = 2  // the list's star field's attribute byte, after the selection field
	clNameCol    = 4  // the list's name's attribute byte, after the star field

	// Field names: a kind, then the checklist's or item's ID.
	clSelField  = "sel:"
	clStarField = "star:"
	clNameField = "name:"
	clDoneField = "done:"
	clItemField = "item:"
	clNewField  = "new:"     // then which blank entry, from 0
	clNewStar   = "newstar:" // a blank checklist's star field, then which one

	clListPrompt = "S opens, * stars. Type over a name to rename it; blank it to remove it."
	clItemPrompt = "X marks done. Type over an item to change it; blank it to remove it."
)

// errChecklistGone reports that the checklist shown was removed by another
// session.
var errChecklistGone = errors.New("that checklist has been removed")

// checklistState is one session's place on the checklist screens.
type checklistState struct {
	// owner is the ID of the user whose checklists are shown, and who owns
	// those added; zero for a session with no user.
	owner int

	// open is the ID of the checklist shown, zero for the list of them;
	// openName is its name, as last drawn.
	open     int
	openName string

	listPage, itemPage int

	// shown is what each input field held when last drawn, so that only
	// what has been typed over is changed. rowIDs are the checklists on
	// the list's rows, or the items on a checklist's, by row, for acting
	// on the one under the cursor.
	shown  map[string]string
	rowIDs map[int]int

	// typed is what was typed when it could not be saved, drawn again to
	// be fixed.
	typed map[string]string

	// held, while set, is a key held for confirmation of what was typed
	// with it, which is shown in place of the screen.
	held *heldKey

	// follow is an item just moved, which the next redraw shows, with the
	// cursor on it, so that it can be moved again.
	follow int

	// confirmReset shows the confirmation for unchecking every item of the
	// checklist open, in place of the checklist.
	confirmReset bool

	// others is how many other sessions have the checklist open, as of
	// this redraw.
	others int

	// cursorOnNew puts the cursor on the first blank entry, after entries
	// have been added from the blank ones.
	cursorOnNew bool

	message string
	isError bool
}

// heldKey is a key held until what was typed with it is confirmed: PF3,
// going back with anything typed, or any key on the list with a name typed
// over or blanked. It is the key, with every field's value, what the
// fields showed, and which entry was on each row.
type heldKey struct {
	resp   go3270.Response
	shown  map[string]string
	rowIDs map[int]int
}

// leaving is whether the key held is PF3, going back.
func (h *heldKey) leaving() bool { return h.resp.AID == go3270.AIDPF3 }

// checklistRows is how many entries, or blank ones for adding, fit on one
// page of a checklist screen.
func checklistRows(rows int) int {
	return max(rows-clFirstRow-3, 1)
}

// pageRange is which of n entries, perPage at a time, are on page, clamped
// to the pages that exist: the page shown, the page count, and the entries'
// bounds.
func pageRange(n, perPage, page int) (shown, total, start, end int) {
	total = max((n+perPage-1)/perPage, 1)
	shown = min(max(page, 0), total-1)
	start = shown * perPage
	return shown, total, start, min(start+perPage, n)
}

// cutRunes shortens s to at most n runes. An input field shows its content
// as it is, so it gets no marker for the cut.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:max(n, 0)])
}

// fieldValue records stored as what the field name shows, and returns what
// to draw in it: what was typed there if it could not be saved, else stored.
func (c *checklistState) fieldValue(name, stored string) string {
	c.shown[name] = stored
	if v, ok := c.typed[name]; ok {
		return v
	}
	return stored
}

// buildChecklist renders the checklist open, or the list of them, and where
// the cursor goes. A checklist removed by another session gives way to the
// list.
func buildChecklist(rows, cols int, now time.Time, lists []checklist.Checklist, loadErr error, c *checklistState) (screen go3270.Screen, cursorRow, cursorCol int) {
	c.shown, c.rowIDs = map[string]string{}, map[int]int{}
	lists = checklist.Owned(lists, c.owner)
	if c.held != nil {
		return buildHeldConfirm(rows, cols, now, lists, c.open, c.held), rows - 1, 0
	}
	if c.open != 0 {
		if i := slices.IndexFunc(lists, func(l checklist.Checklist) bool { return l.ID == c.open }); i >= 0 {
			if c.confirmReset {
				return buildResetConfirm(rows, cols, now, lists[i]), rows - 1, 0
			}
			return buildChecklistItems(rows, cols, now, lists[i], c)
		}
		if loadErr == nil {
			c.message, c.isError = "That checklist has been removed.", true
		}
		c.open, c.typed, c.confirmReset = 0, nil, false
	}
	return buildChecklistList(rows, cols, now, lists, loadErr, c)
}

// buildChecklistList renders one page of the checklists, the last page
// ending with one blank entry for adding another.
func buildChecklistList(rows, cols int, now time.Time, lists []checklist.Checklist, loadErr error, c *checklistState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "CHECKLISTS", now)
	lists = listOrder(lists)
	perPage := checklistRows(rows)
	followed := slices.IndexFunc(lists, func(l checklist.Checklist) bool { return l.ID == c.follow })
	if followed >= 0 {
		c.listPage = followed / perPage
	}
	c.follow = 0
	shownPage, totalPages, start, end := pageRange(len(lists)+1, perPage, c.listPage)
	c.listPage = shownPage

	header := line{{Content: "CHECKLISTS", Color: go3270.Turquoise, Intense: true}}
	if loadErr != nil {
		header = append(header, go3270.Field{Content: loadErr.Error(), Color: go3270.Red})
	} else {
		header = append(header, go3270.Field{Content: countText(len(lists), "checklist", shownPage, totalPages), Color: go3270.Blue})
	}
	screen = append(screen, placeLine(clHeaderRow, cols, header)...)

	// Aligned with a checklist row: the selection in column 1, the star in
	// column 3, the name from column 5, the count right aligned. The first
	// row's field in column 0 stops the underline.
	headings := fmt.Sprintf("%-*s%*s", cols-clCountWidth, "S * Name", clCountWidth-1, "Done")
	screen = append(screen, go3270.Field{
		Row: clColumnRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore, Content: headings,
	})
	nameWidth := cols - clCountWidth - clNameCol - 1
	for i := start; i < end; i++ {
		row := clFirstRow + i - start
		if i >= len(lists) {
			screen = appendNewEntry(screen, row, i-len(lists), clNameCol, cols-clCountWidth, true, c)
			continue
		}
		l := lists[i]
		c.rowIDs[row] = l.ID
		id := strconv.Itoa(l.ID)
		done := 0
		for _, it := range l.Items {
			if it.Done {
				done++
			}
		}
		star, nameColor, nameIntense := "", go3270.Turquoise, false
		countColor := go3270.Green
		if done < len(l.Items) {
			countColor = go3270.Red
		}
		if l.Active {
			star, nameColor, nameIntense = "*", countColor, true
		}
		screen = append(screen,
			go3270.Field{
				Row: row, Col: 0, Write: true, Name: clSelField + id, Content: c.fieldValue(clSelField+id, ""),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{
				Row: row, Col: clStarCol, Write: true, Name: clStarField + id, Content: c.fieldValue(clStarField+id, star),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{
				Row: row, Col: clNameCol, Write: true, Name: clNameField + id,
				Content: c.fieldValue(clNameField+id, cutRunes(l.Name, nameWidth)),
				Color:   nameColor, Intense: nameIntense, Highlighting: go3270.Underscore,
			},
			go3270.Field{
				Row: row, Col: cols - clCountWidth, Color: countColor,
				Content: fmt.Sprintf("%*s", clCountWidth-1, fmt.Sprintf("%d/%d", done, len(l.Items))),
			},
		)
	}
	screen = appendMessageRows(screen, rows, cols, c.message, c.isError, clListPrompt,
		"PF3=Back PF7=Up PF8=Down PF10/11=Move Enter=Save, open selected")
	cursorRow, cursorCol = entryCursor(followed, start, end, len(lists), clNameCol, c)
	return screen, cursorRow, cursorCol
}

// appendNewEntry adds blank entry k for adding one on row, its input
// field's attribute byte in column col, ended at column stop, with a star
// field before it for a checklist.
func appendNewEntry(screen go3270.Screen, row, k, col, stop int, star bool, c *checklistState) go3270.Screen {
	name := clNewField + strconv.Itoa(k)
	// Ends the field before, as an entry's first field would.
	screen = append(screen, go3270.Field{Row: row, Col: 0})
	if star {
		starName := clNewStar + strconv.Itoa(k)
		screen = append(screen, go3270.Field{
			Row: row, Col: clStarCol, Write: true, Name: starName, Content: c.typed[starName],
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		})
	}
	return append(screen,
		go3270.Field{
			Row: row, Col: col, Write: true, Name: name, Content: c.typed[name],
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: row, Col: stop},
	)
}

// entryCursor is where the cursor goes on a page showing entries start to
// end of n, then any blank ones for adding, whose input fields' attribute
// bytes are in column newCol: to the entry just moved (followed, -1 for
// none), else the first on the page, else (or just after adding) the first
// blank entry.
func entryCursor(followed, start, end, n, newCol int, c *checklistState) (row, col int) {
	switch {
	case followed >= 0:
		return clFirstRow + followed - start, 1
	case start < n && !c.cursorOnNew:
		return clFirstRow, 1
	case end > n:
		return clFirstRow + n - start, newCol + 1
	}
	return clFirstRow, 1
}

// listOrder is lists in the order the list shows them: those starred first,
// then the rest, each in the order kept. Unstarring a checklist puts it
// back where it was.
func listOrder(lists []checklist.Checklist) []checklist.Checklist {
	out := make([]checklist.Checklist, 0, len(lists))
	for _, active := range []bool{true, false} {
		for _, l := range lists {
			if l.Active == active {
				out = append(out, l)
			}
		}
	}
	return out
}

// buildChecklistItems renders one page of the items of l, the last page
// filled out with blank entries for adding more.
func buildChecklistItems(rows, cols int, now time.Time, l checklist.Checklist, c *checklistState) (screen go3270.Screen, cursorRow, cursorCol int) {
	c.openName = l.Name
	screen = titleFields(cols, "CHECKLIST", now)
	perPage := checklistRows(rows)
	followed := slices.IndexFunc(l.Items, func(it checklist.Item) bool { return it.ID == c.follow })
	if followed >= 0 {
		c.itemPage = followed / perPage
	}
	c.follow = 0
	shownPage, totalPages, start, end := pageRange(len(l.Items)+1, perPage, c.itemPage)
	c.itemPage = shownPage
	if end > len(l.Items) {
		end = start + perPage // the last page, filled out with blank entries
	}

	done := 0
	for _, it := range l.Items {
		if it.Done {
			done++
		}
	}
	info := fmt.Sprintf("%d of %d done", done, len(l.Items))
	if totalPages > 1 {
		info += fmt.Sprintf(", page %d/%d", shownPage+1, totalPages)
	}
	header := line{
		{Content: l.Name, Color: go3270.Turquoise, Intense: true},
		{Content: info, Color: go3270.Blue},
	}
	if c.others > 0 {
		noun := "sessions"
		if c.others == 1 {
			noun = "session"
		}
		header = append(header, go3270.Field{
			Content: fmt.Sprintf("(%d other %s viewing)", c.others, noun), Color: go3270.Pink, Intense: true,
		})
	}
	screen = append(screen, placeLine(clHeaderRow, cols, header)...)

	// Aligned with an item row: the done mark in column 1, the item from
	// column 3. The first row's field in column 0 stops the underline.
	const headings = "S Item"
	screen = append(screen, go3270.Field{
		Row: clColumnRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
		Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
	})
	// An item runs to the last column, where a field ends it.
	textWidth := cols - clItemCol - 2
	for i := start; i < end; i++ {
		row := clFirstRow + i - start
		if i >= len(l.Items) {
			screen = appendNewEntry(screen, row, i-len(l.Items), clItemCol, cols-1, false, c)
			continue
		}
		it := l.Items[i]
		c.rowIDs[row] = it.ID
		id := strconv.Itoa(it.ID)
		mark := ""
		if it.Done {
			mark = "X"
		}
		// Items still to do stand out in red; those done are dimmer.
		color, intense := go3270.Red, true
		if it.Done {
			color, intense = go3270.Green, false
		}
		screen = append(screen,
			go3270.Field{
				Row: row, Col: 0, Write: true, Name: clDoneField + id, Content: c.fieldValue(clDoneField+id, mark),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{
				Row: row, Col: clItemCol, Write: true, Name: clItemField + id,
				Content: c.fieldValue(clItemField+id, cutRunes(it.Text, textWidth)),
				Color:   color, Intense: intense, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: cols - 1},
		)
	}
	screen = appendMessageRows(screen, rows, cols, c.message, c.isError, clItemPrompt,
		"PF3=Back PF6=Reset all PF7=Up PF8=Down PF10/11=Move Enter=Save")
	cursorRow, cursorCol = entryCursor(followed, start, end, len(l.Items), clItemCol, c)
	return screen, cursorRow, cursorCol
}

// buildResetConfirm renders the confirmation for unchecking every item of
// l, listing those checked.
func buildResetConfirm(rows, cols int, now time.Time, l checklist.Checklist) go3270.Screen {
	screen := titleFields(cols, "RESET CHECKLIST", now)
	var checked []string
	for _, it := range l.Items {
		if it.Done {
			checked = append(checked, it.Text)
		}
	}
	screen = append(screen, placeLine(clHeaderRow, cols, line{
		{Content: "Uncheck every item of", Color: go3270.Yellow, Intense: true},
		{Content: l.Name + "?", Color: go3270.Turquoise, Intense: true},
	})...)
	screen = append(screen, placeLine(clHeaderRow+1, cols, line{
		{Content: countText(len(checked), "item", 0, 1) + " of " + strconv.Itoa(len(l.Items)) + " checked:", Color: go3270.Blue},
	})...)

	// The list runs from below the heading to above the prompt.
	limit := max(rows-3-(clHeaderRow+3), 1)
	shown := checked
	if len(shown) > limit {
		shown = shown[:limit-1]
	}
	row := clHeaderRow + 3
	for _, text := range shown {
		screen = append(screen, placeLineAt(row, clItemCol, cols, line{{Content: text, Color: go3270.Green}})...)
		row++
	}
	if len(shown) < len(checked) {
		screen = append(screen, placeLineAt(row, clItemCol, cols, line{{Content: fmt.Sprintf("... and %d more", len(checked)-len(shown)), Color: go3270.Blue}})...)
	}

	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to uncheck them all, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Uncheck all", cols-1)})
}

// buildHeldConfirm renders the confirmation for what was typed with the key
// held, on the list or, with open, on that checklist: when going back,
// whether to save it first, else whether to change and remove the
// checklists or items typed over.
func buildHeldConfirm(rows, cols int, now time.Time, lists []checklist.Checklist, open int, held *heldKey) go3270.Screen {
	edits, marks, bad := typedChanges(held.shown, held.resp.Values)
	changes, renames, removes := describeChanges(lists, open, edits, marks, typedAdds(held.resp.Values))
	if bad != "" {
		changes = append([]line{{{Content: bad, Color: go3270.Red, Intense: true}}}, changes...)
	}

	title, question := "SAVE CHANGES", "Save these changes before going back?"
	prompt := "Press PF4 to save and go back, PF12 to discard and go back, or PF3 to return."
	help := "PF3=Return PF4=Save and go back PF12=Discard and go back"
	if !held.leaving() {
		noun, verb := "checklist", "rename "
		if open != 0 {
			noun, verb = "item", "change "
		}
		title, question = "CHANGE "+strings.ToUpper(noun)+"S", "Those "+noun+"s have been removed by another session."
		prompt = "Press PF4 to save, PF3 to go back to what was typed, or PF12 to discard it."
		help = "PF3=Back PF4=Save PF12=Discard"
		var asked []string
		if renames > 0 {
			asked = append(asked, verb+countText(renames, noun, 0, 1))
		}
		if removes > 0 {
			asked = append(asked, "remove "+countText(removes, noun, 0, 1))
		}
		if len(asked) > 0 {
			q := strings.Join(asked, " and ")
			question = strings.ToUpper(q[:1]) + q[1:] + "?"
		}
	}
	screen := titleFields(cols, title, now)
	screen = append(screen, placeLine(clHeaderRow, cols, line{{Content: question, Color: go3270.Yellow, Intense: true}})...)

	// The list runs from below the heading to above the prompt.
	limit := max(rows-3-(clHeaderRow+2), 1)
	shown := changes
	if len(shown) > limit {
		shown = shown[:limit-1]
	}
	row := clHeaderRow + 2
	for _, l := range shown {
		screen = append(screen, placeLineAt(row, clItemCol, cols, l)...)
		row++
	}
	if len(shown) < len(changes) {
		screen = append(screen, placeLineAt(row, clItemCol, cols, line{{Content: fmt.Sprintf("... and %d more", len(changes)-len(shown)), Color: go3270.Blue}})...)
	}

	screen = append(screen, placeLine(rows-2, cols, line{{Content: prompt, Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate(help, cols-1)})
}

// describeChanges is a line for each change typed, with how many names or
// items were changed and how many removed: on the list (open zero) the
// names changed or blanked, and the stars; on checklist open, the items
// changed or blanked, and the marks; then on either, what was typed to add. Entries another session
// has removed since are left out.
func describeChanges(lists []checklist.Checklist, open int, edits map[int]string, marks map[int]bool, added []newEntry) (changes []line, renames, removes int) {
	change := func(verb string, color go3270.Color, l line) {
		changes = append(changes, append(line{{Content: verb, Color: color, Intense: true}}, l...))
	}
	if open == 0 {
		for _, l := range listOrder(lists) {
			if name, ok := edits[l.ID]; ok && name == "" {
				removes++
				done := 0
				for _, it := range l.Items {
					if it.Done {
						done++
					}
				}
				change("Remove", go3270.Red, line{
					{Content: l.Name, Color: go3270.Turquoise, Intense: true},
					{Content: fmt.Sprintf("(%s, %d done)", countText(len(l.Items), "item", 0, 1), done), Color: go3270.Blue},
				})
			} else if ok {
				renames++
				change("Rename", go3270.Yellow, line{
					{Content: l.Name, Color: go3270.Turquoise},
					{Content: "to", Color: go3270.Blue},
					{Content: name, Color: go3270.Turquoise, Intense: true},
				})
			}
			if star, ok := marks[l.ID]; ok {
				verb := "Unstar"
				if star {
					verb = "Star"
				}
				change(verb, go3270.Yellow, line{{Content: l.Name, Color: go3270.Turquoise}})
			}
		}
	} else if i := slices.IndexFunc(lists, func(l checklist.Checklist) bool { return l.ID == open }); i >= 0 {
		for _, it := range lists[i].Items {
			if text, ok := edits[it.ID]; ok && text == "" {
				removes++
				change("Remove", go3270.Red, line{{Content: it.Text, Color: go3270.Turquoise, Intense: true}})
			} else if ok {
				renames++
				change("Change", go3270.Yellow, line{
					{Content: it.Text, Color: go3270.Turquoise},
					{Content: "to", Color: go3270.Blue},
					{Content: text, Color: go3270.Turquoise, Intense: true},
				})
			}
			if done, ok := marks[it.ID]; ok {
				verb := "Uncheck"
				if done {
					verb = "Check"
				}
				change(verb, go3270.Yellow, line{{Content: it.Text, Color: go3270.Turquoise}})
			}
		}
	}
	for _, a := range added {
		l := line{{Content: a.text, Color: go3270.Turquoise, Intense: true}}
		if a.star {
			l = append(l, go3270.Field{Content: "(starred)", Color: go3270.Blue})
		}
		change("Add", go3270.Green, l)
	}
	return changes, renames, removes
}

// newEntry is what was typed on a blank entry: the name or item, and for a
// checklist, whether its star field was typed in.
type newEntry struct {
	text string
	star bool
}

// typedAdds is what was typed on the blank entries for adding, in the order
// shown. A star typed beside no name adds nothing.
func typedAdds(values map[string]string) []newEntry {
	var ks []int
	for name, v := range values {
		if k, ok := strings.CutPrefix(name, clNewField); ok && v != "" {
			if n, err := strconv.Atoi(k); err == nil {
				ks = append(ks, n)
			}
		}
	}
	slices.Sort(ks)
	added := make([]newEntry, len(ks))
	for i, k := range ks {
		added[i] = newEntry{values[clNewField+strconv.Itoa(k)], values[clNewStar+strconv.Itoa(k)] != ""}
	}
	return added
}

// countText is "n nouns", with the page when there is more than one.
func countText(n int, noun string, page, totalPages int) string {
	if n != 1 {
		noun += "s"
	}
	s := fmt.Sprintf("%d %s", n, noun)
	if totalPages > 1 {
		s += fmt.Sprintf(", page %d/%d", page+1, totalPages)
	}
	return s
}

// handleChecklist acts on a key pressed on a checklist screen, returning
// whether to leave for the dashboard. Whatever was typed is saved first,
// whatever the key; if it cannot be, it is left on the screen to fix and the
// key does nothing more. Then PF3 goes back (to the list from a checklist),
// PF10 and PF11 move the entry the key acts on (see target) up and down,
// PF6 asks to uncheck every item of the checklist, PF7 and PF8 page, and
// Enter on the list opens the checklist selected, wherever the cursor is.
//
// Going back with PF3 with anything typed (but a selection) asks first
// whether to save it, as does any key with a name or item typed over or
// blanked. There, PF4 saves it all, then if the key was PF3 or Enter, does
// what that key does; any other key does nothing more. PF12 discards it,
// then goes back if the key was PF3. PF3 returns to the screen with it all
// still typed, to change.
//
// On the confirmation for unchecking, only PF4 unchecks; PF3 goes back, and
// any other key leaves the confirmation up.
func (c *checklistState) handle(resp go3270.Response, store *checklist.Store) (leave bool) {
	return c.handleKey(resp, store, false)
}

// handleKey is handle, with confirmed set when the key is being acted on
// again once what was typed with it has been confirmed.
func (c *checklistState) handleKey(resp go3270.Response, store *checklist.Store, confirmed bool) (leave bool) {
	c.message, c.isError, c.typed, c.cursorOnNew = "", false, nil, false
	if held := c.held; held != nil {
		switch resp.AID {
		case go3270.AIDPF3:
			c.held, c.typed = nil, held.resp.Values
			c.message = "Nothing is saved yet. Change what was typed, then press Enter."
		case go3270.AIDPF4:
			c.held, c.shown, c.rowIDs = nil, held.shown, held.rowIDs
			if held.leaving() || held.resp.AID == go3270.AIDEnter {
				return c.handleKey(held.resp, store, true)
			}
			if _, err := c.save(held.resp.Values, store); err != nil {
				c.typed, c.message, c.isError = held.resp.Values, "Could not save: "+err.Error(), true
			}
		case go3270.AIDPF12:
			c.held = nil
			switch {
			case !held.leaving():
				c.message = "Discarded what was typed."
			case c.open == 0:
				return true
			default:
				c.open, c.itemPage = 0, 0
			}
		}
		return false
	}
	if c.confirmReset {
		switch resp.AID {
		case go3270.AIDPF3:
			c.confirmReset = false
		case go3270.AIDPF4:
			c.confirmReset = false
			if err := uncheckAll(store, c.open); err != nil {
				c.message, c.isError = "Could not save: "+err.Error(), true
			} else {
				c.message = "Every item is unchecked."
			}
		}
		return false
	}
	values := resp.Values
	if !confirmed {
		edits, marks, bad := typedChanges(c.shown, values)
		unsaved := bad != "" || len(edits) > 0 || len(marks) > 0 || len(typedAdds(values)) > 0
		if resp.AID == go3270.AIDPF3 && unsaved || len(edits) > 0 {
			c.held = &heldKey{resp: resp, shown: c.shown, rowIDs: c.rowIDs}
			return false
		}
	}
	selected := c.selected(values)

	bad, err := c.save(values, store)
	switch {
	case errors.Is(err, errChecklistGone):
		c.open, c.message, c.isError = 0, "That checklist has been removed.", true
		return false
	case err != nil:
		bad = "Could not save: " + err.Error()
	}
	if bad != "" {
		c.typed, c.message, c.isError = resp.Values, bad, true
		return false
	}

	page := &c.listPage
	if c.open != 0 {
		page = &c.itemPage
	}
	switch resp.AID {
	case go3270.AIDPF3:
		if c.open == 0 {
			return true
		}
		c.open, c.itemPage = 0, 0
	case go3270.AIDPF4:
		c.message, c.isError = "Type over a name to rename the checklist, or blank it to remove it.", true
		if c.open != 0 {
			c.message = "Type over an item to change it, or blank it to remove it."
		}
	case go3270.AIDPF10, go3270.AIDPF11:
		id, ok := c.target(resp, selected, "PF10 or PF11")
		if !ok {
			break
		}
		by := -1
		if resp.AID == go3270.AIDPF11 {
			by = 1
		}
		if msg, err := moveEntry(store, c.owner, c.open, id, by); err != nil {
			c.message, c.isError = "Could not save: "+err.Error(), true
		} else {
			c.message, c.isError = msg, msg != ""
		}
		c.follow = id
	case go3270.AIDPF6:
		if c.open == 0 {
			break
		}
		// Asked of the checklist as saved just now, with any marks typed.
		lists, err := store.Load()
		i := slices.IndexFunc(lists, func(l checklist.Checklist) bool { return l.ID == c.open })
		switch {
		case err != nil:
			c.message, c.isError = err.Error(), true
		case i >= 0 && !slices.ContainsFunc(lists[i].Items, func(it checklist.Item) bool { return it.Done }):
			c.message = "Nothing is checked."
		default:
			c.confirmReset = true // a checklist gone goes to the list
		}
	case go3270.AIDPF7:
		*page = max(*page-1, 0)
	case go3270.AIDPF8:
		*page++ // the next redraw keeps it to the pages there are
	case go3270.AIDEnter:
		switch {
		case len(selected) > 1:
			c.selectOne(selected)
		case len(selected) == 1:
			// Not if its name was just blanked, which removed it.
			if name, ok := values[clNameField+strconv.Itoa(selected[0])]; !ok || name != "" {
				c.open, c.itemPage = selected[0], 0
			}
		}
	}
	return false
}

// selected are the checklists selected on the list, in the order shown.
func (c *checklistState) selected(values map[string]string) []int {
	if c.open != 0 {
		return nil
	}
	var ids []int
	for row := clFirstRow; row < clFirstRow+len(c.rowIDs); row++ {
		if values[clSelField+strconv.Itoa(c.rowIDs[row])] != "" {
			ids = append(ids, c.rowIDs[row])
		}
	}
	return ids
}

// selectOne reports that the key needs one checklist selected, not all of
// selected, which are left selected to fix.
func (c *checklistState) selectOne(selected []int) {
	c.message, c.isError = "Select one checklist at a time.", true
	c.typed = map[string]string{}
	for _, id := range selected {
		c.typed[clSelField+strconv.Itoa(id)] = "S"
	}
}

// target is what a key acting on one entry acts on, reporting on the
// screen when there is none: on the list, the checklist selected, else the
// one under the cursor; on a checklist, the item under the cursor.
func (c *checklistState) target(resp go3270.Response, selected []int, key string) (id int, ok bool) {
	switch {
	case len(selected) == 1:
		return selected[0], true
	case len(selected) > 1:
		c.selectOne(selected)
		return 0, false
	}
	if id, ok := c.rowIDs[resp.Row]; ok {
		return id, true
	}
	if c.open == 0 {
		c.message = "Type S beside a checklist, or put the cursor on one, then press " + key + "."
	} else {
		c.message = "Put the cursor on an item, then press " + key + "."
	}
	c.isError = true
	return 0, false
}

// typedChanges is what values has typed over the fields drawn, which
// showed what drawn has by field name: new names or item texts by ID (""
// to remove), and new done marks by item ID or stars by checklist ID. bad reports the first entry that cannot be used. Any
// character typed in a star field stars the checklist, and blanking it
// unstars it. Selection fields are not saved.
func typedChanges(drawn, values map[string]string) (edits map[int]string, marks map[int]bool, bad string) {
	edits, marks = map[int]string{}, map[int]bool{}
	for name, shown := range drawn {
		v, ok := values[name]
		if !ok || v == shown {
			continue
		}
		kind, idText, _ := strings.Cut(name, ":")
		id, _ := strconv.Atoi(idText)
		switch kind + ":" {
		case clDoneField:
			switch mark := strings.ToUpper(v); {
			case mark != "" && mark != "X":
				return nil, nil, fmt.Sprintf("Type X beside an item done, or leave it blank; not %q.", v)
			case mark != shown:
				marks[id] = mark == "X"
			}
		case clNameField, clItemField:
			edits[id] = v
		case clStarField:
			if star := v != ""; star != (shown != "") {
				marks[id] = star
			}
		}
	}
	return edits, marks, ""
}

// save applies what was typed over the fields drawn last, and what was
// typed on the blank entries for adding. Nothing is saved if anything typed cannot
// be; bad then says why.
func (c *checklistState) save(values map[string]string, store *checklist.Store) (bad string, err error) {
	edits, marks, bad := typedChanges(c.shown, values)
	if bad != "" {
		return bad, nil
	}
	added := typedAdds(values)
	if len(edits) == 0 && len(marks) == 0 && len(added) == 0 {
		return "", nil
	}

	removed, changed := 0, 0
	open := c.open
	err = store.Update(func(lists *[]checklist.Checklist, nextID func() int) error {
		removed, changed = 0, 0
		if open == 0 {
			*lists = slices.DeleteFunc(*lists, func(l checklist.Checklist) bool {
				v, ok := edits[l.ID]
				if ok && v == "" {
					removed++
				}
				return ok && v == ""
			})
			for i, l := range *lists {
				if v, ok := edits[l.ID]; ok {
					(*lists)[i].Name = v
					changed++
				}
				if active, ok := marks[l.ID]; ok {
					(*lists)[i].Active = active
				}
			}
			for _, a := range added {
				*lists = append(*lists, checklist.Checklist{ID: nextID(), Name: a.text, Active: a.star, Owner: c.owner})
			}
			return nil
		}

		i := slices.IndexFunc(*lists, func(l checklist.Checklist) bool { return l.ID == open })
		if i < 0 {
			return errChecklistGone
		}
		l := &(*lists)[i]
		l.Items = slices.DeleteFunc(l.Items, func(it checklist.Item) bool {
			v, ok := edits[it.ID]
			if ok && v == "" {
				removed++
			}
			return ok && v == ""
		})
		for j := range l.Items {
			it := &l.Items[j]
			if v, ok := edits[it.ID]; ok {
				it.Text = v
				changed++
			}
			if done, ok := marks[it.ID]; ok {
				it.Done = done
			}
		}
		for _, a := range added {
			l.Items = append(l.Items, checklist.Item{ID: nextID(), Text: a.text})
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	noun, verb := "item", "Changed "
	if open == 0 {
		noun, verb = "checklist", "Renamed "
	}
	var done []string
	if changed > 0 {
		done = append(done, verb+countText(changed, noun, 0, 1)+".")
	}
	if removed > 0 {
		done = append(done, "Removed "+countText(removed, noun, 0, 1)+".")
	}
	c.message = strings.Join(done, " ")
	if len(added) > 0 {
		// Show the new entries, at the end, and stay ready to add more.
		c.cursorOnNew = true
		if open == 0 {
			c.listPage = math.MaxInt
		} else {
			c.itemPage = math.MaxInt
		}
	}
	return "", nil
}

// moveEntry moves owner's checklist id, or with open item id of checklist
// open, by one place, up (by -1) or down (by 1). It returns why it could not
// when that is the entry's place, not an error: already at the top or
// bottom, or gone.
func moveEntry(store *checklist.Store, owner, open, id, by int) (msg string, err error) {
	err = store.Update(func(lists *[]checklist.Checklist, _ func() int) error {
		if open == 0 {
			msg = moveChecklist(*lists, owner, id, by)
			return nil
		}
		i := slices.IndexFunc(*lists, func(l checklist.Checklist) bool { return l.ID == open })
		if i < 0 {
			return errChecklistGone
		}
		msg = moveByID((*lists)[i].Items, func(it checklist.Item) int { return it.ID }, id, by, "item")
		return nil
	})
	if errors.Is(err, errChecklistGone) {
		return "", nil // the next redraw goes to the list and says so
	}
	return msg, err
}

// moveChecklist moves owner's checklist id past their next unstarred
// checklist up (by -1) or down (by 1) the list, or says why it cannot. The
// starred are listed apart, first, so it skips over them, and they cannot
// be moved; other users' checklists are not on the list at all.
func moveChecklist(lists []checklist.Checklist, owner, id, by int) string {
	j := slices.IndexFunc(lists, func(l checklist.Checklist) bool { return l.ID == id && l.Owner == owner })
	switch {
	case j < 0:
		return "That checklist has been removed."
	case lists[j].Active:
		return "Starred checklists stay at the top; unstar one to move it."
	}
	k := j + by
	for k >= 0 && k < len(lists) && (lists[k].Active || lists[k].Owner != owner) {
		k += by
	}
	switch {
	case k < 0:
		return "That checklist is already at the top."
	case k >= len(lists):
		return "That checklist is already at the bottom."
	}
	lists[j], lists[k] = lists[k], lists[j]
	return ""
}

// moveByID swaps the entry of s with ID id with the one by places from it,
// or says why it cannot.
func moveByID[T any](s []T, idOf func(T) int, id, by int, noun string) string {
	j := slices.IndexFunc(s, func(e T) bool { return idOf(e) == id })
	switch {
	case j < 0:
		return "That " + noun + " has been removed."
	case j+by < 0:
		return "That " + noun + " is already at the top."
	case j+by >= len(s):
		return "That " + noun + " is already at the bottom."
	}
	s[j], s[j+by] = s[j+by], s[j]
	return ""
}

// uncheckAll unchecks every item of checklist open.
func uncheckAll(store *checklist.Store, open int) error {
	err := store.Update(func(lists *[]checklist.Checklist, _ func() int) error {
		i := slices.IndexFunc(*lists, func(l checklist.Checklist) bool { return l.ID == open })
		if i < 0 {
			return errChecklistGone
		}
		for j := range (*lists)[i].Items {
			(*lists)[i].Items[j].Done = false
		}
		return nil
	})
	if errors.Is(err, errChecklistGone) {
		return nil // the next redraw goes to the list and says so
	}
	return err
}
