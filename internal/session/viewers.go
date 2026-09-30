package session

import "sync"

// Viewers tracks which checklist each session has open, so that each can
// say how many others are looking at the same one. Only sessions of this
// server are counted.
type Viewers struct {
	mu   sync.Mutex
	open map[uint64]int // checklist IDs by session
}

// NewViewers returns an empty Viewers.
func NewViewers() *Viewers {
	return &Viewers{open: map[uint64]int{}}
}

// Set records that session has checklist open, zero for none, and returns
// how many other sessions have it open too. A nil Viewers counts nobody.
func (v *Viewers) Set(session uint64, checklist int) (others int) {
	if v == nil {
		return 0
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if checklist == 0 {
		delete(v.open, session)
		return 0
	}
	v.open[session] = checklist
	for s, c := range v.open {
		if s != session && c == checklist {
			others++
		}
	}
	return others
}
