package registry

// Offered is every declared tool the model may be shown: all of Specs but the
// deferred, which keep their policy and docs while nothing runs them (SPEC
// §14). A fresh map, so a caller may narrow it further.
func Offered() map[string]ToolSpec {
	out := make(map[string]ToolSpec, len(Specs))
	for name, spec := range Specs {
		if !spec.Deferred {
			out[name] = spec
		}
	}
	return out
}
