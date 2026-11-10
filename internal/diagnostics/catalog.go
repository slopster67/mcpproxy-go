package diagnostics

// Get looks up a catalog entry by code. Returns (zero, false) for unknown codes.
func Get(c Code) (CatalogEntry, bool) {
	e, ok := registry[c]
	return e, ok
}
