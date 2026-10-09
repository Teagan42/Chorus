package ui

// Doc is the dot for the "doc-start" layout partial.
type Doc struct {
	Title  string
	Static string // URL prefix where Static() is mounted, e.g. "/static"
	Notice *Notice
}
