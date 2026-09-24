package singlebinary

import (
	"github.com/withHumanAI/withHuman/runtime"
	"github.com/withHumanAI/withHuman/webapp"
)

// openEdition is what cmd/withhuman composes: core's own frontend bundle
// and no edition options.
func openEdition() runtime.ServerOptions {
	return runtime.ServerOptions{Frontend: webapp.Assets()}
}
