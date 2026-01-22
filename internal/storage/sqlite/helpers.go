package sqlite

// b2i converts bool to 0/1 for SQLite.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nilIfEmpty returns nil for empty strings so INSERT sets NULL instead of "".
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
