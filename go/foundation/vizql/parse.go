package vizql

import (
	"bytes"
	"encoding/json"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/listquery"
)

// MaxShelfDepth bounds a shelf expression's nesting (a leaf counts as one level),
// so a crafted deep spec is a clean CodeInvalidInput, not unbounded recursion.
const MaxShelfDepth = 8

// grains is the set of accepted time resolutions.
var grains = map[types.Grain]bool{
	types.GrainMinute: true, types.GrainHour: true, types.GrainDay: true, types.GrainMonth: true,
}

// marks is the set of accepted explicit marks (MarkAuto included).
var marks = map[types.Mark]bool{
	types.MarkAuto: true, types.MarkText: true, types.MarkBar: true, types.MarkLine: true, types.MarkPoint: true,
}

// ops is the set of accepted table-algebra operators.
var ops = map[types.AlgebraOp]bool{
	types.AlgebraCross: true, types.AlgebraConcat: true, types.AlgebraNest: true,
}

// invalid builds the CodeInvalidInput error every rejection returns.
func invalid(msg string) error {
	return errors.New(errors.CodeInvalidInput, "vizspec: "+msg)
}

// Parse decodes a JSON VizSpec and validates it against the cube's allow-list m:
// every referenced field must be allow-listed; a measure must carry one of its
// permitted aggregates and a dimension or time field none; detail fields must be
// dimensions; shelves nest at most MaxShelfDepth levels; the grain, mark, time
// range must be valid; a sort key must name a dimension or aggregated measure the
// spec places (shelves, detail, encodings); a nil m is rejected; and the filter is compiled with
// listquery.Parse. Every rejection is CodeInvalidInput.
func Parse(b []byte, m *listquery.Map) (types.VizSpec, error) {
	var spec types.VizSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		if errors.Code(err) == errors.CodeUnknown {
			return types.VizSpec{}, errors.Wrap(err, errors.CodeInvalidInput, "vizspec: invalid JSON")
		}
		return types.VizSpec{}, err
	}
	if err := validate(&spec, m); err != nil {
		return types.VizSpec{}, err
	}
	return spec, nil
}

// validate checks a decoded spec against m and compiles its filter in place.
func validate(spec *types.VizSpec, m *listquery.Map) error {
	if err := validateScalars(spec); err != nil {
		return err
	}
	if m == nil {
		return invalid("cube has no field allow-list")
	}
	keys, err := validateFields(spec, m)
	if err != nil {
		return err
	}
	for _, o := range spec.Sort {
		if !keys[o.Field] {
			return invalid("sort key " + o.Field + " is not a shelved dimension or aggregated measure")
		}
	}
	return compileFilter(spec, m)
}

// validateScalars checks the cube, grain, mark and time range.
func validateScalars(spec *types.VizSpec) error {
	if spec.Cube == "" {
		return invalid("cube is required")
	}
	if spec.Grain != "" && !grains[spec.Grain] {
		return invalid("unknown grain " + string(spec.Grain))
	}
	if !marks[spec.Mark] {
		return invalid("unknown mark " + string(spec.Mark))
	}
	if tr := spec.TimeRange; !tr.From.IsZero() && !tr.To.IsZero() && !tr.From.Before(tr.To) {
		return invalid("time_range.from must precede time_range.to")
	}
	return nil
}

// validateFields checks every field reference (shelves, detail, encodings) and
// returns the set of their Keys, which sort directives may name.
func validateFields(spec *types.VizSpec, m *listquery.Map) (map[string]bool, error) {
	keys := map[string]bool{}
	for _, shelf := range []types.Shelf{spec.Rows, spec.Columns} {
		if err := validateShelf(shelf, m, 1, keys); err != nil {
			return nil, err
		}
	}
	for _, ref := range spec.Detail {
		f, ok := m.Lookup(ref.Name)
		if !ok || f.Role == listquery.RoleMeasure || ref.Agg != types.AggNone {
			return nil, invalid("detail field " + ref.Name + " is not a declared dimension")
		}
		keys[ref.Key()] = true
	}
	for _, ref := range spec.Encodings.Fields() {
		if err := validateRef(ref, m); err != nil {
			return nil, err
		}
		keys[ref.Key()] = true
	}
	return keys, nil
}

// compileFilter parses the spec's wire filter with listquery.Parse into Filter and
// keeps FilterJSON compact, so a parsed spec re-serializes canonically.
func compileFilter(spec *types.VizSpec, m *listquery.Map) error {
	if len(spec.FilterJSON) == 0 {
		return nil
	}
	filter, err := listquery.Parse(m, string(spec.FilterJSON))
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, spec.FilterJSON); err != nil {
		return errors.Wrap(err, errors.CodeInvalidInput, "vizspec: invalid filter JSON")
	}
	spec.Filter, spec.FilterJSON = filter, compact.Bytes()
	return nil
}

// validateShelf walks one shelf expression, bounding its depth and validating
// each leaf; it records every leaf's Key in keys.
func validateShelf(e *types.AlgebraExpr, m *listquery.Map, depth int, keys map[string]bool) error {
	if e == nil {
		return nil
	}
	if depth > MaxShelfDepth {
		return invalid("shelf nesting exceeds max depth")
	}
	if e.Field != nil {
		keys[e.Field.Key()] = true
		return validateRef(*e.Field, m)
	}
	if !ops[e.Op] {
		return invalid("unknown shelf operator " + string(e.Op))
	}
	if len(e.Args) == 0 {
		return invalid("shelf operator " + string(e.Op) + " needs operands")
	}
	for _, arg := range e.Args {
		if arg == nil {
			return invalid("null shelf operand")
		}
		if err := validateShelf(arg, m, depth+1, keys); err != nil {
			return err
		}
	}
	return nil
}

// validateRef checks one field reference against m: allow-listed, and aggregated
// iff it is a measure, with a permitted aggregate.
func validateRef(ref types.FieldRef, m *listquery.Map) error {
	f, ok := m.Lookup(ref.Name)
	if !ok {
		return invalid("unknown field " + ref.Name)
	}
	if f.Role == listquery.RoleMeasure {
		if ref.Agg == types.AggNone {
			return invalid("measure " + ref.Name + " needs an aggregate")
		}
		if !f.Allows(ref.Agg) {
			return invalid("aggregate " + string(ref.Agg) + " is not permitted on " + ref.Name)
		}
		return nil
	}
	if ref.Agg != types.AggNone {
		return invalid("aggregate " + string(ref.Agg) + " on non-measure " + ref.Name)
	}
	return nil
}
