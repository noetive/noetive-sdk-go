package semantik

// applyNamespaceDefaults fills in Model and Dimensions when the
// effective namespace is [DefaultNamespace] and the caller left those
// fields zero. This lets a user send a minimal `{Query}` request and
// get the globally provisioned embedding configuration for free.
//
// The function is shared by Search, Publish and Subscribe to keep
// defaulting logic in one place.
func applyNamespaceDefaults(namespace string, model *string, dims *uint16) {
	if namespace != DefaultNamespace {
		return
	}
	if *model == "" {
		*model = DefaultModel
	}
	if *dims == 0 {
		*dims = DefaultDimensions
	}
}
