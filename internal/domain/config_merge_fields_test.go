package domain

import (
	"reflect"
	"slices"
	"testing"
)

// mergeDecisions names, for every field of Config, how OverlayOnto treats it.
// It exists so a new Config field cannot ship without somebody deciding how it
// inherits through `extends:` — the omission that once let a child's signing,
// forge, agent_trace, status and push blocks vanish without a trace.
var mergeDecisions = map[string]string{
	"Extends":         "resolved by the loader; cleared in the result",
	"Profile":         "ratchets: the higher of base and child; the loader refuses a lower child",
	"Agent":           "child wins when set",
	"Hooks":           "child replaces when any hook is named",
	"Execution":       "field level, child wins",
	"Commands":        "per key, child wins",
	"Timeouts":        "per key, child wins",
	"Cache":           "per key, child wins",
	"PredictiveCache": "child replaces when set",
	"Diagnostics":     "per key, child wins",
	"SymlinkDeps":     "child wins when set",
	"MaterializeDeps": "child replaces when set (deprecated)",
	"Writes":          "union",
	"AgentCommands":   "per key, child wins",
	"Steps":           "per hook union",
	"Parallel":        "child wins when set",
	"Notify":          "child wins when set",
	"NotifyAfter":     "child wins when set",
	"Risk":            "field level, child wins",
	"SecurityScan":    "field level; mode cannot weaken",
	"PR":              "child replaces when set",
	"Status":          "field level, child wins",
	"Push":            "field level, child wins",
	"TrustedKeys":     "union",
	"Forge":           "field level, child wins (accept_authored included)",
	"Signing":         "field level, child wins; required only ratchets up",
	"AgentTrace":      "field level, child wins; required only ratchets up",
	"Rules":           "base then child",
}

func TestOverlayOnto_EveryConfigFieldHasAMergeDecision(t *testing.T) {
	var fields []string
	for _, f := range configFields() {
		fields = append(fields, f.Name)
		if _, ok := mergeDecisions[f.Name]; !ok {
			t.Errorf("Config.%s has no merge decision: decide how it inherits through extends in OverlayOnto, then record it in mergeDecisions", f.Name)
		}
	}
	for name := range mergeDecisions {
		if !slices.Contains(fields, name) {
			t.Errorf("mergeDecisions names %s, which Config no longer has", name)
		}
	}
}

func configFields() []reflect.StructField {
	typ := reflect.TypeFor[Config]()
	out := make([]reflect.StructField, 0, typ.NumField())
	for i := range typ.NumField() {
		out = append(out, typ.Field(i))
	}
	return out
}

// fillNonZero sets every exported field reachable from v to a non-zero value,
// so a merge that forgets a field (or a sub-field) shows up as a difference.
func fillNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillNonZero(t, p.Elem())
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(t, s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillNonZero(t, k)
		e := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(t, e)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillNonZero(t, v.Field(i))
			}
		}
	default:
		t.Fatalf("fillNonZero: unhandled kind %s (%s)", v.Kind(), v.Type())
	}
}

// configWithOnly returns a Config whose only non-zero field is name, filled
// down to every sub-field.
func configWithOnly(t *testing.T, name string) Config {
	t.Helper()
	var c Config
	fillNonZero(t, reflect.ValueOf(&c).Elem().FieldByName(name))
	return c
}

// Every field a child sets must reach the merged config, and every field only
// the base sets must survive. Driven by reflection, so a field added later is
// covered without anyone remembering to write a test for it.
func TestOverlayOnto_EveryFieldInheritsAndOverrides(t *testing.T) {
	for _, f := range configFields() {
		if f.Name == "Extends" {
			continue // resolved by the loader, cleared below
		}
		t.Run(f.Name, func(t *testing.T) {
			set := configWithOnly(t, f.Name)
			want := reflect.ValueOf(set).FieldByName(f.Name).Interface()

			childOnly := set.OverlayOnto(Config{})
			if got := reflect.ValueOf(childOnly).FieldByName(f.Name).Interface(); !reflect.DeepEqual(got, want) {
				t.Errorf("child-only %s was not applied:\n got  %#v\n want %#v", f.Name, got, want)
			}
			baseOnly := Config{}.OverlayOnto(set)
			if got := reflect.ValueOf(baseOnly).FieldByName(f.Name).Interface(); !reflect.DeepEqual(got, want) {
				t.Errorf("base-only %s was not inherited:\n got  %#v\n want %#v", f.Name, got, want)
			}
		})
	}
	if got := (Config{Extends: "base.yaml"}).OverlayOnto(Config{Extends: "root.yaml"}); got.Extends != "" {
		t.Errorf("merged config must not still extend: %q", got.Extends)
	}
}

func TestOverlayOnto_SigningFieldLevel(t *testing.T) {
	base := Config{Signing: SigningConfig{Signer: "ssh", SSHKey: "~/.ssh/org.pub"}}
	child := Config{Signing: SigningConfig{SSHKey: "~/.ssh/repo.pub", Required: true}}
	got := child.OverlayOnto(base).Signing
	want := SigningConfig{Signer: "ssh", SSHKey: "~/.ssh/repo.pub", Required: true}
	if got != want {
		t.Errorf("signing = %+v, want %+v", got, want)
	}
}

func TestOverlayOnto_AgentTraceFieldLevel(t *testing.T) {
	base := Config{AgentTrace: AgentTraceConfig{Path: ".agent-trace.json"}}
	child := Config{AgentTrace: AgentTraceConfig{Path: "trace/record.json", Required: true}}
	got := child.OverlayOnto(base).AgentTrace
	want := AgentTraceConfig{Path: "trace/record.json", Required: true}
	if got != want {
		t.Errorf("agent_trace = %+v, want %+v", got, want)
	}
	inherit := Config{AgentTrace: AgentTraceConfig{Required: true}}.OverlayOnto(base).AgentTrace
	if inherit.Path != ".agent-trace.json" || !inherit.Required {
		t.Errorf("a child setting only required must keep the base path: %+v", inherit)
	}
}

// The two `required` switches are the repo (or org) saying a weaker outcome is
// not acceptable. A child may add that strictness; it must not be able to drop
// an org base's by writing `required: false` — the same rule security_scan.mode
// and trusted_keys follow.
func TestOverlayOnto_RequiredOnlyRatchetsUp(t *testing.T) {
	cases := []struct {
		name        string
		base, child bool
		want        bool
	}{
		{"neither", false, false, false},
		{"child adds it", false, true, true},
		{"base keeps it", true, false, true},
		{"both", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := Config{Signing: SigningConfig{Required: tc.base}, AgentTrace: AgentTraceConfig{Path: "t.json", Required: tc.base}}
			child := Config{Signing: SigningConfig{Required: tc.child}, AgentTrace: AgentTraceConfig{Required: tc.child}}
			got := child.OverlayOnto(base)
			if got.Signing.Required != tc.want {
				t.Errorf("signing.required = %t, want %t", got.Signing.Required, tc.want)
			}
			if got.AgentTrace.Required != tc.want {
				t.Errorf("agent_trace.required = %t, want %t", got.AgentTrace.Required, tc.want)
			}
		})
	}
}

// accept_authored follows the child in both directions: a repo can opt in under
// a base that did not, and — because turning it off is the stricter choice — a
// repo can opt out under a base that opted in.
func TestOverlayOnto_ForgeFollowsTheChild(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name        string
		base, child *bool
		want        ForgePolicy
	}{
		{"unset everywhere", nil, nil, ForgeReject},
		{"base only", &yes, nil, ForgeAccept},
		{"child opts in", &no, &yes, ForgeAccept},
		{"child opts out", &yes, &no, ForgeReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := Config{Forge: ForgeConfig{AcceptAuthored: tc.base}}
			child := Config{Forge: ForgeConfig{AcceptAuthored: tc.child}}
			if got := child.OverlayOnto(base).Forge.Policy(); got != tc.want {
				t.Errorf("policy = %v, want %v", got, tc.want)
			}
		})
	}
	keys := Config{Forge: ForgeConfig{Keys: []string{"CHILD"}}}.OverlayOnto(
		Config{Forge: ForgeConfig{AcceptAuthored: &yes, Keys: []string{"BASE"}}}).Forge
	if !reflect.DeepEqual(keys.ForgeKeys(), []string{"CHILD"}) || keys.Policy() != ForgeAccept {
		t.Errorf("child keys must replace the base's and keep its accept_authored: %+v", keys)
	}
}

func TestOverlayOnto_StatusFieldLevel(t *testing.T) {
	yes, no := true, false
	base := Config{Status: StatusConfig{Enabled: &yes, Context: "org/gate"}}

	ctx := Config{Status: StatusConfig{Context: "repo/gate"}}.OverlayOnto(base).Status
	if !ctx.IsEnabled() || ctx.StatusContext() != "repo/gate" {
		t.Errorf("a child setting only context must keep the base's enabled: %+v", ctx)
	}
	off := Config{Status: StatusConfig{Enabled: &no}}.OverlayOnto(base).Status
	if off.IsEnabled() || off.StatusContext() != "org/gate" {
		t.Errorf("a child must be able to switch status off and keep the context: %+v", off)
	}
	on := Config{Status: StatusConfig{Enabled: &yes}}.OverlayOnto(Config{}).Status
	if !on.IsEnabled() {
		t.Error("a child switching status on must win")
	}
}

func TestOverlayOnto_PushFieldLevel(t *testing.T) {
	base := Config{Push: &PushConfig{Force: ForceNever}}
	if got := (Config{Push: &PushConfig{Force: ForceLease}}).OverlayOnto(base).PushForceMode(); got != ForceLease {
		t.Errorf("child push.force must win: %q", got)
	}
	if got := (Config{Push: &PushConfig{}}).OverlayOnto(base).PushForceMode(); got != ForceNever {
		t.Errorf("an empty child push block must inherit the base's force: %q", got)
	}
	if got := (Config{}).OverlayOnto(base).PushForceMode(); got != ForceNever {
		t.Errorf("base push.force must be inherited: %q", got)
	}
	merged := (Config{Push: &PushConfig{Force: ForceLease}}).OverlayOnto(base)
	if base.Push.Force != ForceNever || merged.Push == base.Push {
		t.Error("merging must not alias or mutate the base's push block")
	}
}
