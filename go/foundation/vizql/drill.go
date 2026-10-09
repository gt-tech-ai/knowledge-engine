package vizql

import "github.com/gt-tech-ai/knowledge-engine/go/core/types"

// Drill returns a copy of spec with the dimension field replaced, wherever it is
// used as a dimension (shelves, detail, encodings), by its child in the cube's
// hierarchy. The input spec is not modified. A field in no hierarchy, at a
// hierarchy's last level, or whose child the cube does not declare is CodeInvalidInput.
func Drill(spec types.VizSpec, cube CubeSchema, field string) (types.VizSpec, error) {
	child, ok := childOf(cube.Hierarchies, field)
	if !ok {
		return types.VizSpec{}, invalid("field " + field + " has no child level to drill into")
	}
	if _, declared := cube.Fields.Lookup(child); !declared {
		return types.VizSpec{}, invalid("drill child " + child + " is not declared by the cube")
	}
	swap := func(ref types.FieldRef) types.FieldRef {
		if ref.Name == field && ref.Agg == types.AggNone {
			ref.Name = child
		}
		return ref
	}
	out := spec
	out.Rows, out.Columns = drillShelf(spec.Rows, swap), drillShelf(spec.Columns, swap)
	out.Detail = make([]types.FieldRef, len(spec.Detail))
	for i, ref := range spec.Detail {
		out.Detail[i] = swap(ref)
	}
	if spec.Detail == nil {
		out.Detail = nil
	}
	enc := []**types.FieldRef{&out.Encodings.Color, &out.Encodings.Size, &out.Encodings.Shape, &out.Encodings.Label}
	for _, slot := range enc {
		if *slot != nil {
			swapped := swap(**slot)
			*slot = &swapped
		}
	}
	return out, nil
}

// childOf returns the level after field in the first hierarchy containing it.
func childOf(hierarchies [][]string, field string) (string, bool) {
	for _, path := range hierarchies {
		for i, level := range path {
			if level == field && i+1 < len(path) {
				return path[i+1], true
			}
		}
	}
	return "", false
}

// drillShelf deep-copies a shelf expression, applying swap to every leaf.
func drillShelf(e *types.AlgebraExpr, swap func(types.FieldRef) types.FieldRef) *types.AlgebraExpr {
	if e == nil {
		return nil
	}
	if e.Field != nil {
		ref := swap(*e.Field)
		return &types.AlgebraExpr{Field: &ref}
	}
	args := make([]*types.AlgebraExpr, len(e.Args))
	for i, arg := range e.Args {
		args[i] = drillShelf(arg, swap)
	}
	return &types.AlgebraExpr{Op: e.Op, Args: args}
}
