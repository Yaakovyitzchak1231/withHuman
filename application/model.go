package application

import "errors"

// ModelIdentity is the provider and model one adapter calls. Core calls
// no model itself; it is the vocabulary an edition's model adapters (the
// hosted AI judge) share.
type ModelIdentity struct {
	Provider string
	Model    string
}

// ErrModelUnavailable is a transient provider failure (rate limit,
// overload, network) an adapter reports so the caller can say so or try
// again within its own budget.
var ErrModelUnavailable = errors.New("model is temporarily unavailable")

func (identity ModelIdentity) String() string {
	return identity.Provider + "/" + identity.Model
}
