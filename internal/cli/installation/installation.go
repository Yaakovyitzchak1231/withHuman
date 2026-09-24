// Package installation connects provider enrollment to the public AAP library.
package installation

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/agentapprovalprotocol/agentapprovalprotocol/adapters"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/cli/credential"
)

func New(configDir string, env adapters.Environment) (*adapters.Manager, error) {
	env.ConfigDir = filepath.Join(configDir, "aap")
	return adapters.NewWithEnvironment(env)
}

// Origin validates the provider origin before any credential-bearing request.
func Origin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("provider URL must be an HTTP(S) origin without credentials, a path, query, or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", fmt.Errorf("provider URL requires HTTPS except on localhost")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Install stores the one-time enrollment before installation so local failures
// can be repaired without enrolling another instance. Secrets never enter reports.
func Install(manager *adapters.Manager, configDir, key string, cred credential.Credential) (adapters.InstallResult, error) {
	adapter, err := manager.Lookup(key)
	if err != nil {
		return adapters.InstallResult{}, err
	}
	origin, err := Origin(cred.ProviderURL)
	if err != nil {
		return adapters.InstallResult{}, err
	}
	cred.ProviderURL, cred.Runtime = origin, key
	if cred.Token == "" {
		return adapters.InstallResult{}, fmt.Errorf("enrollment returned no instance credential")
	}
	if _, err := credential.Save(configDir, key, cred); err != nil {
		return adapters.InstallResult{}, err
	}
	// A failed registration can leave plugin files in place. Remove its
	// recorded changes before retrying, so unchanged assets cannot be mistaken
	// for a successful runtime registration by the pinned library.
	status, err := adapter.Status()
	if err != nil {
		return adapters.InstallResult{Adapter: key}, err
	}
	if status.Configured && !status.Complete {
		if err := adapter.Uninstall(); err != nil {
			return adapters.InstallResult{Adapter: key}, err
		}
	}
	return adapter.Install(cred.Token, origin+"/api/aap")
}

func Report(result adapters.InstallResult, err error) domain.OnboardingInstallResult {
	report := domain.OnboardingInstallResult{Runtime: result.Adapter, OK: result.Complete && err == nil, Notes: result.Notes}
	for _, change := range result.Changes {
		report.Changes = append(report.Changes, domain.OnboardingFileChange{Path: change.Path, Action: change.Action})
	}
	if err != nil {
		report.Message = err.Error()
	}
	return report
}
