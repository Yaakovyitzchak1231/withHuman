package runtime

import "github.com/withHumanAI/withHuman/config"

// loadConfig is the one place a composition picks between the
// environment and a configuration handed in by the caller.
func loadConfig(override *config.Config) (config.Config, error) {
	if override != nil {
		return *override, nil
	}
	return config.Load()
}
