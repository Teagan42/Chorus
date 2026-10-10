package hass

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// MaxEntities bounds one ha_find_entities answer. A real home has hundreds of
// entities, and an unfiltered dump is prompt the model has to read aloud past.
const MaxEntities = 25

// Tools wires every declared ha_ tool to c. Argument shapes are read from the
// generated registry at call time, so what the model is offered and what is
// accepted here are one declaration (SPEC §6).
func Tools(c *Client) map[string]session.Tool {
	return map[string]session.Tool{
		"ha_call_service":  classified{tool("ha_call_service", c.callService), c.classify},
		"ha_get_state":     tool("ha_get_state", c.getState),
		"ha_find_entities": tool("ha_find_entities", c.findEntities),
	}
}

type handler func(ctx context.Context, a *args) (any, error)

// tool checks arguments against the declaration before the handler sees them.
// A failure is an error, which the session records as a result the model
// reasons about (SPEC §7).
func tool(name string, h handler) session.Tool {
	spec, declared := registry.Specs[name]
	return session.ToolFunc(func(ctx context.Context, raw string) (string, error) {
		if !declared {
			return "", fmt.Errorf("%s is not declared in the registry", name)
		}
		a, err := parseArgs(spec, raw)
		if err != nil {
			return "", err
		}
		out, err := h(ctx, a)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("%s: encode result: %w", name, err)
		}
		return string(b), nil
	})
}

// classified is a tool that can say what a call acts on before it runs, so
// the session can hold the garage door and not the blinds (ADR-0041).
type classified struct {
	session.Tool
	classify func(ctx context.Context, raw string) ([]string, error)
}

// Classify reports the classes of what the call acts on.
func (t classified) Classify(ctx context.Context, raw string) ([]string, error) {
	return t.classify(ctx, raw)
}

// --------------------------------------------------------------- arguments

// args is a call's arguments, checked against the declared parameters: no
// unknown field, no missing required one, every value of its declared type.
type args struct {
	spec   registry.ToolSpec
	fields map[string]json.RawMessage
}

func parseArgs(spec registry.ToolSpec, raw string) (*args, error) {
	fields := map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return nil, fmt.Errorf("bad arguments: %w", err)
		}
	}
	declared := make(map[string]registry.ParamSpec, len(spec.Params))
	for _, p := range spec.Params {
		declared[p.Name] = p
	}
	for name, v := range fields {
		p, ok := declared[name]
		if !ok {
			return nil, fmt.Errorf("bad arguments: unknown field %q", name)
		}
		kind := jsonKind(v)
		// A model that fills every slot sends null for the ones it has no
		// value for; that is absence, not a type error.
		if kind == "null" {
			delete(fields, name)
			continue
		}
		if kind != p.Type && !(p.Type == "integer" && kind == "number") {
			return nil, fmt.Errorf("bad arguments: %q must be a %s", name, p.Type)
		}
	}
	for _, p := range spec.Params {
		if _, ok := fields[p.Name]; p.Required && !ok {
			return nil, fmt.Errorf("bad arguments: %q is required", p.Name)
		}
	}
	return &args{spec: spec, fields: fields}, nil
}

// jsonKind names a value's JSON type from its first byte.
func jsonKind(v json.RawMessage) string {
	s := strings.TrimSpace(string(v))
	switch {
	case s == "":
		return "null"
	case s[0] == '"':
		return "string"
	case s[0] == '{':
		return "object"
	case s[0] == '[':
		return "array"
	case s == "true", s == "false":
		return "boolean"
	case s == "null":
		return "null"
	default:
		return "number"
	}
}

// str reads an optional string. Reading a name the schema does not declare is
// a drift between this file and schema/tool.cue, and is reported as one.
func (a *args) str(name string) (string, error) {
	v, ok, err := a.lookup(name)
	if err != nil || !ok {
		return "", err
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("bad arguments: %q: %w", name, err)
	}
	return s, nil
}

func (a *args) object(name string) (map[string]any, error) {
	v, ok, err := a.lookup(name)
	if err != nil || !ok {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(v, &m); err != nil {
		return nil, fmt.Errorf("bad arguments: %q: %w", name, err)
	}
	return m, nil
}

func (a *args) lookup(name string) (json.RawMessage, bool, error) {
	for _, p := range a.spec.Params {
		if p.Name == name {
			v, ok := a.fields[name]
			return v, ok, nil
		}
	}
	return nil, false, fmt.Errorf("%s reads undeclared parameter %q", a.spec.Name, name)
}

// ---------------------------------------------------------------- handlers

// identifier is an HA domain or service name. Checked here because either one
// is a path segment, and a slash in it would address a different endpoint.
var identifier = regexp.MustCompile(`^[a-z0-9_]+$`)

// entityID is HA's domain.object_id.
var entityID = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// targetKeys are the service-data fields Home Assistant reads a target from.
// ha_call_service takes its target as entity_id or area_id, and nowhere else.
var targetKeys = []string{"entity_id", "area_id", "device_id", "floor_id", "label_id"}

type changedState struct {
	EntityID string `json:"entity_id"`
	State    string `json:"state"`
}

type changedResult struct {
	Changed []changedState `json:"changed"`
}

func (c *Client) callService(ctx context.Context, a *args) (any, error) {
	domain, err := a.ident("domain")
	if err != nil {
		return nil, err
	}
	service, err := a.ident("service")
	if err != nil {
		return nil, err
	}
	entity, err := a.str("entity_id")
	if err != nil {
		return nil, err
	}
	area, err := a.str("area_id")
	if err != nil {
		return nil, err
	}
	if entity == "" && area == "" {
		return nil, fmt.Errorf("bad arguments: entity_id or area_id is required")
	}
	if entity != "" && !entityID.MatchString(entity) {
		return nil, fmt.Errorf("bad arguments: %q %q is not domain.object_id", "entity_id", entity)
	}
	data, err := a.object("data")
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	// A target in data would be acted on beside the one the gate classified.
	for _, k := range targetKeys {
		if _, ok := data[k]; ok {
			return nil, fmt.Errorf("bad arguments: data.%s: name the target in entity_id or area_id", k)
		}
	}
	if entity != "" {
		data["entity_id"] = entity
	}
	if area != "" {
		data["area_id"] = area
	}

	changed, err := c.CallService(ctx, domain, service, data)
	if err != nil {
		return nil, err
	}
	out := changedResult{Changed: make([]changedState, 0, len(changed))}
	for _, s := range changed {
		out.Changed = append(out.Changed, changedState{EntityID: s.EntityID, State: s.State})
	}
	return out, nil
}

// classify reads the device_class Home Assistant gives the entity a call
// names: "garage" for the garage door, nothing for a plain window. A target
// it cannot read is an error, which holds the call: an area's members are
// not on the REST API (see Client.States).
func (c *Client) classify(ctx context.Context, raw string) ([]string, error) {
	var a struct {
		EntityID string         `json:"entity_id"`
		AreaID   string         `json:"area_id"`
		Data     map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("classify: bad arguments: %w", err)
	}
	if a.AreaID != "" {
		return nil, fmt.Errorf("classify: area %q: its entities are not readable over REST", a.AreaID)
	}
	for _, k := range targetKeys {
		if _, ok := a.Data[k]; ok {
			return nil, fmt.Errorf("classify: %q in data names a target", k)
		}
	}
	if !entityID.MatchString(a.EntityID) {
		return nil, fmt.Errorf("classify: %q %q is not domain.object_id", "entity_id", a.EntityID)
	}
	st, err := c.EntityState(ctx, a.EntityID)
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	if class, _ := st.Attributes["device_class"].(string); class != "" {
		return []string{class}, nil
	}
	return nil, nil
}

func (a *args) ident(name string) (string, error) {
	s, err := a.str(name)
	if err != nil {
		return "", err
	}
	if !identifier.MatchString(s) {
		return "", fmt.Errorf("bad arguments: %q %q is not a service identifier", name, s)
	}
	return s, nil
}

type stateResult struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed,omitempty"`
}

func (c *Client) getState(ctx context.Context, a *args) (any, error) {
	entity, err := a.str("entity_id")
	if err != nil {
		return nil, err
	}
	if !entityID.MatchString(entity) {
		return nil, fmt.Errorf("bad arguments: %q %q is not domain.object_id", "entity_id", entity)
	}
	st, err := c.EntityState(ctx, entity)
	if err != nil {
		return nil, err
	}
	return stateResult{
		EntityID: st.EntityID, State: st.State,
		Attributes: scalars(st.Attributes), LastChanged: st.LastChanged,
	}, nil
}

// scalars keeps the attributes a spoken answer is made of. Lists and nested
// objects -- supported colour modes, a 48-hour forecast -- are dropped.
func scalars(attrs map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range attrs {
		switch v.(type) {
		case string, float64, bool:
			out[k] = v
		}
	}
	return out
}

type entity struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	State    string `json:"state"`
}

type findResult struct {
	Entities  []entity `json:"entities"`
	Truncated bool     `json:"truncated,omitempty"`
}

// findEntities filters /api/states by domain prefix and a case-folded
// substring of the friendly name or the id. Areas are not filterable here:
// /api/states does not carry them (see Client.States).
func (c *Client) findEntities(ctx context.Context, a *args) (any, error) {
	domain, err := a.str("domain")
	if err != nil {
		return nil, err
	}
	name, err := a.str("name")
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(name)

	all, err := c.States(ctx)
	if err != nil {
		return nil, err
	}
	out := findResult{Entities: []entity{}}
	for _, s := range all {
		if domain != "" && !strings.HasPrefix(s.EntityID, domain+".") {
			continue
		}
		friendly, _ := s.Attributes["friendly_name"].(string)
		if friendly == "" {
			friendly = s.EntityID
		}
		if needle != "" && !strings.Contains(strings.ToLower(friendly), needle) &&
			!strings.Contains(strings.ToLower(s.EntityID), needle) {
			continue
		}
		out.Entities = append(out.Entities, entity{EntityID: s.EntityID, Name: friendly, State: s.State})
	}
	// Sorted before the cut, so the same home always pages the same way.
	sort.Slice(out.Entities, func(i, j int) bool { return out.Entities[i].EntityID < out.Entities[j].EntityID })
	if len(out.Entities) > MaxEntities {
		out.Entities = out.Entities[:MaxEntities]
		out.Truncated = true
	}
	return out, nil
}
