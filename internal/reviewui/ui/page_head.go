package ui

// PageHead is the band under the app header: step eyebrow, optional switch
// and meta, the title, a subtitle, then stats and actions on the right.
type PageHead struct {
	Eyebrow      string  // "02 · Triage"
	Switch       *Switch // Curate uses this for DPO pairs | Hard negatives
	Meta         string  // small mono text beside the eyebrow
	MetaTone     Tone
	MetaHref     string
	Title        string
	Trace        bool // smaller title for trace screens (22px instead of 30px)
	Subtitle     string
	SubtitleMono bool
	Stats        []Metric // compact numbers beside the actions
	Actions      []Button
	Aside        *Transport // trace screens put the transport here instead of stats
}
