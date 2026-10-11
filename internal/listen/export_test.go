package listen

// Announcing reports an announcement nobody may answer still playing. Its
// close is journalled a moment before it ends, and a wake waits for the end.
func (l *Listener) Announcing() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.announcingLocked() != nil
}
