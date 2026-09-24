// withhuman-openapi-permissions rewrites the x-withhuman-permissions block
// of the HTTP contract from the code-owned permission registry. Run it
// (make openapi-permissions) after changing domain/permissions.go; the
// conformance test in internal/httpapi fails until the committed block
// matches.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/withHumanAI/withHuman/internal/httpapi"
)

func main() {
	spec := flag.String("spec", "internal/httpapi/openapi.yaml", "path to the OpenAPI document to rewrite")
	flag.Parse()
	if err := run(*spec); err != nil {
		fmt.Fprintln(os.Stderr, "withhuman-openapi-permissions:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rendered, err := httpapi.RenderSpecPermissions(current)
	if err != nil {
		return err
	}
	rendered = httpapi.RenderSpecCredentialKinds(rendered)
	if string(rendered) == string(current) {
		fmt.Println("permission vocabulary already up to date")
		return nil
	}
	if err := os.WriteFile(path, rendered, info.Mode().Perm()); err != nil {
		return err
	}
	fmt.Println("permission vocabulary written to", path)
	return nil
}
