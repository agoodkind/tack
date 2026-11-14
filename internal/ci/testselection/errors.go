package testselection

import "errors"

var (
	errTagsRequired      = errors.New("require -tags with at least one additional build tag")
	errTagName           = errors.New("require a single tag name")
	errPatternsRequired  = errors.New("require at least one relative package pattern")
	errModuleFile        = errors.New("require a go.mod file")
	errRelativePattern   = errors.New("require a relative path with an optional /... suffix")
	errModulePath        = errors.New("require a path inside the module")
	errPackageDirectory  = errors.New("does not identify a directory")
	errExcludedDirectory = errors.New("does not belong to a package pattern")
	errNestedModule      = errors.New("belongs to a nested module")
)
