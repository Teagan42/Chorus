package ui

// ListRow is one conversation in Triage or Browse: why it’s here, what was
// said, its shape, who and where, a figure (first-audio latency) and when.
type ListRow struct {
	Href       string
	Tag        SigTag
	Title      string
	Detail     string // mono line under the title
	Thumb      *ConvThumb
	Who        string
	Figure     string
	FigureTone Tone // e.g. ToneVoice when latency is over target
	When       string
}

// List renders a header row and ListRows. Columns label the six cells; set
// Empty to show an empty state when Rows is empty. ID makes it an htmx target.
type List struct {
	ID       string
	Columns  [6]string // signal, utterance, shape, who, figure, when
	Rows     []ListRow
	Empty    *EmptyState
	MinWidth int // px before horizontal scroll; default 1040
}

func (l List) MinW() int {
	if l.MinWidth > 0 {
		return l.MinWidth
	}
	return 1040
}
