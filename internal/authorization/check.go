package authorization

import "github.com/withHumanAI/withHuman/authz"

// The permission checks live in the exported authz package so an
// edition's services use the same ones; the evaluators here keep their
// callers' names.
type Denied = authz.Denied

var (
	Check         = authz.Check
	Scope         = authz.Scope
	HoldsAnywhere = authz.HoldsAnywhere
	holds         = authz.Holds
)
