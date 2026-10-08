package ui

// Cell is one table cell. Mono for figures and hashes; Href makes it a link;
// Muted greys it; New marks a row added this session.
type Cell struct {
	Text  string
	Mono  bool
	Muted bool
	Href  string
}

// DataTable is a plain hairline table, e.g. previous exports. Columns can be
// given CSS grid track sizes in Widths (default: equal).
type DataTable struct {
	ID      string
	Label   string
	Columns []string
	Widths  string // e.g. "90px 150px 90px 110px minmax(0,1fr) 130px"
	Rows    []TableRow
	Empty   *EmptyState
}

type TableRow struct {
	Cells []Cell
	New   bool
}
