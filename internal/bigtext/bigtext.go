// Package bigtext draws text in large characters made of # (which every
// code page has), each Height rows high and Width columns wide, for
// banners and countdowns on a character screen.
//
// Capitals and digits fill all seven rows. Lowercase letters stand on the
// same baseline, their bodies two rows shorter: those with ascenders reach
// the top row, and those with descenders are raised so that their tails
// fit in the bottom row.
package bigtext

import "strings"

// The size of a character.
const (
	Height = 7
	Width  = 5
)

// Glyph is one character: Height rows, each Width columns, # for ink and
// a space for none.
type Glyph [Height]string

// blank is a character with no ink: a space, and what an unknown
// character is drawn as.
var blank = Glyph{"     ", "     ", "     ", "     ", "     ", "     ", "     "}

// Lookup returns r's glyph, and whether the font has one.
func Lookup(r rune) (Glyph, bool) {
	g, ok := font[r]
	return g, ok
}

// Supports reports whether the font has a glyph for every character of
// text.
func Supports(text string) bool {
	for _, r := range text {
		if _, ok := font[r]; !ok {
			return false
		}
	}
	return true
}

// TextWidth is how many columns text takes drawn by Rows: Width for each
// character, with a column between each two.
func TextWidth(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return n*(Width+1) - 1
}

// Rows draws text, each of its rows the characters' rows with a column
// between each two. A character the font has no glyph for is drawn blank.
func Rows(text string) [Height]string {
	var b [Height]strings.Builder
	for i, r := range []rune(text) {
		g, ok := font[r]
		if !ok {
			g = blank
		}
		for row := range Height {
			if i > 0 {
				b[row].WriteByte(' ')
			}
			b[row].WriteString(g[row])
		}
	}
	var out [Height]string
	for row := range Height {
		out[row] = b[row].String()
	}
	return out
}

// font is every character drawn.
var font = map[rune]Glyph{
	' ': blank,

	'0': {" ### ", "#   #", "#  ##", "# # #", "##  #", "#   #", " ### "},
	'1': {"  #  ", " ##  ", "  #  ", "  #  ", "  #  ", "  #  ", " ### "},
	'2': {" ### ", "#   #", "    #", "   # ", "  #  ", " #   ", "#####"},
	'3': {"#### ", "    #", "    #", " ### ", "    #", "    #", "#### "},
	'4': {"   # ", "  ## ", " # # ", "#  # ", "#####", "   # ", "   # "},
	'5': {"#####", "#    ", "#### ", "    #", "    #", "#   #", " ### "},
	'6': {"  ## ", " #   ", "#    ", "#### ", "#   #", "#   #", " ### "},
	'7': {"#####", "    #", "   # ", "  #  ", " #   ", " #   ", " #   "},
	'8': {" ### ", "#   #", "#   #", " ### ", "#   #", "#   #", " ### "},
	'9': {" ### ", "#   #", "#   #", " ####", "    #", "   # ", " ##  "},

	'A': {" ### ", "#   #", "#   #", "#####", "#   #", "#   #", "#   #"},
	'B': {"#### ", "#   #", "#   #", "#### ", "#   #", "#   #", "#### "},
	'C': {" ### ", "#   #", "#    ", "#    ", "#    ", "#   #", " ### "},
	'D': {"#### ", "#   #", "#   #", "#   #", "#   #", "#   #", "#### "},
	'E': {"#####", "#    ", "#    ", "#### ", "#    ", "#    ", "#####"},
	'F': {"#####", "#    ", "#    ", "#### ", "#    ", "#    ", "#    "},
	'G': {" ### ", "#   #", "#    ", "# ###", "#   #", "#   #", " ####"},
	'H': {"#   #", "#   #", "#   #", "#####", "#   #", "#   #", "#   #"},
	'I': {" ### ", "  #  ", "  #  ", "  #  ", "  #  ", "  #  ", " ### "},
	'J': {"  ###", "   # ", "   # ", "   # ", "   # ", "#  # ", " ##  "},
	'K': {"#   #", "#  # ", "# #  ", "##   ", "# #  ", "#  # ", "#   #"},
	'L': {"#    ", "#    ", "#    ", "#    ", "#    ", "#    ", "#####"},
	'M': {"#   #", "## ##", "# # #", "# # #", "#   #", "#   #", "#   #"},
	'N': {"#   #", "##  #", "# # #", "#  ##", "#   #", "#   #", "#   #"},
	'O': {" ### ", "#   #", "#   #", "#   #", "#   #", "#   #", " ### "},
	'P': {"#### ", "#   #", "#   #", "#### ", "#    ", "#    ", "#    "},
	'Q': {" ### ", "#   #", "#   #", "#   #", "# # #", "#  # ", " ## #"},
	'R': {"#### ", "#   #", "#   #", "#### ", "# #  ", "#  # ", "#   #"},
	'S': {" ####", "#    ", "#    ", " ### ", "    #", "    #", "#### "},
	'T': {"#####", "  #  ", "  #  ", "  #  ", "  #  ", "  #  ", "  #  "},
	'U': {"#   #", "#   #", "#   #", "#   #", "#   #", "#   #", " ### "},
	'V': {"#   #", "#   #", "#   #", "#   #", "#   #", " # # ", "  #  "},
	'W': {"#   #", "#   #", "#   #", "# # #", "# # #", "# # #", " # # "},
	'X': {"#   #", "#   #", " # # ", "  #  ", " # # ", "#   #", "#   #"},
	'Y': {"#   #", "#   #", " # # ", "  #  ", "  #  ", "  #  ", "  #  "},
	'Z': {"#####", "    #", "   # ", "  #  ", " #   ", "#    ", "#####"},

	'a': {"     ", "     ", " ### ", "    #", " ####", "#   #", " ####"},
	'b': {"#    ", "#    ", "#### ", "#   #", "#   #", "#   #", "#### "},
	'c': {"     ", "     ", " ####", "#    ", "#    ", "#    ", " ####"},
	'd': {"    #", "    #", " ####", "#   #", "#   #", "#   #", " ####"},
	'e': {"     ", "     ", " ### ", "#   #", "#####", "#    ", " ####"},
	'f': {"  ## ", " #   ", " #   ", "#### ", " #   ", " #   ", " #   "},
	'g': {"     ", " ####", "#   #", "#   #", " ####", "    #", " ### "},
	'h': {"#    ", "#    ", "#### ", "#   #", "#   #", "#   #", "#   #"},
	'i': {"  #  ", "     ", " ##  ", "  #  ", "  #  ", "  #  ", " ### "},
	'j': {"   # ", "     ", "  ## ", "   # ", "   # ", "#  # ", " ##  "},
	'k': {"#    ", "#    ", "#  # ", "# #  ", "##   ", "# #  ", "#  # "},
	'l': {" ##  ", "  #  ", "  #  ", "  #  ", "  #  ", "  #  ", " ### "},
	'm': {"     ", "     ", "## # ", "# # #", "# # #", "# # #", "#   #"},
	'n': {"     ", "     ", "#### ", "#   #", "#   #", "#   #", "#   #"},
	'o': {"     ", "     ", " ### ", "#   #", "#   #", "#   #", " ### "},
	'p': {"     ", "#### ", "#   #", "#   #", "#### ", "#    ", "#    "},
	'q': {"     ", " ####", "#   #", "#   #", " ####", "    #", "    #"},
	'r': {"     ", "     ", "# ## ", "##   ", "#    ", "#    ", "#    "},
	's': {"     ", "     ", " ####", "#    ", " ### ", "    #", "#### "},
	't': {" #   ", " #   ", "#### ", " #   ", " #   ", " #  #", "  ## "},
	'u': {"     ", "     ", "#   #", "#   #", "#   #", "#  ##", " ## #"},
	'v': {"     ", "     ", "#   #", "#   #", "#   #", " # # ", "  #  "},
	'w': {"     ", "     ", "#   #", "#   #", "# # #", "# # #", " # # "},
	'x': {"     ", "     ", "#   #", " # # ", "  #  ", " # # ", "#   #"},
	'y': {"     ", "#   #", "#   #", "#   #", " ####", "    #", " ### "},
	'z': {"     ", "     ", "#####", "   # ", "  #  ", " #   ", "#####"},

	':':  {"     ", "  #  ", "  #  ", "     ", "  #  ", "  #  ", "     "},
	'/':  {"    #", "    #", "   # ", "  #  ", " #   ", "#    ", "#    "},
	'-':  {"     ", "     ", "     ", "#####", "     ", "     ", "     "},
	'.':  {"     ", "     ", "     ", "     ", "     ", " ##  ", " ##  "},
	'!':  {"  #  ", "  #  ", "  #  ", "  #  ", "  #  ", "     ", "  #  "},
	'?':  {" ### ", "#   #", "    #", "   # ", "  #  ", "     ", "  #  "},
	'\'': {"  #  ", "  #  ", "     ", "     ", "     ", "     ", "     "},
}
