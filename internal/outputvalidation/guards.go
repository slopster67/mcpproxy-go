package outputvalidation

// nestingDepth computes the maximum nesting depth of v by walking its structure.
// Scalars (string, number, bool, nil, json.Number) have depth 1.
// A map or slice contributes depth 1 + max(children depths).
// An empty map or slice has depth 1.
func nestingDepth(v any) int {
	deepest := 0
	switch val := v.(type) {
	case map[string]any:
		for _, child := range val {
			deepest = max(deepest, nestingDepth(child))
		}
	case []any:
		for _, child := range val {
			deepest = max(deepest, nestingDepth(child))
		}
	default:
		return 1
	}
	return 1 + deepest
}
