package ui

// FieldKind picks the control: "input", "search", "select", "textarea", "checkbox".
type FieldKind string

const (
	FieldInput    FieldKind = "input"
	FieldSearch   FieldKind = "search"
	FieldSelect   FieldKind = "select"
	FieldTextarea FieldKind = "textarea"
	FieldCheckbox FieldKind = "checkbox"
)

// Field is a labelled form control. Labels are caps labels above the control,
// or beside it when Inline. HiddenLabel keeps the label for screen readers only.
type Field struct {
	Kind        FieldKind
	ID          string
	Name        string
	Label       string
	HiddenLabel bool
	Inline      bool
	Value       string
	Placeholder string
	Rows        int
	Checked     bool
	Options     []Option
	Hx          Hx
}

type Option struct {
	Value    string
	Label    string
	Selected bool
}
