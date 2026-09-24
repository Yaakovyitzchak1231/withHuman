package cli

import (
	"context"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/withHumanAI/withHuman/internal/cli/upgrade"
)

func (a *app) updater() *upgrade.Updater {
	env := a.updateEnv
	if env.Interactive == nil {
		env.Interactive = func() bool {
			input, inOK := a.stdin.(*os.File)
			output, outOK := a.stdout.(*os.File)
			errors, errOK := a.stderr.(*os.File)
			return inOK && outOK && errOK && term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(output.Fd())) && term.IsTerminal(int(errors.Fd()))
		}
	}
	return upgrade.New(upgrade.Options{
		Version: a.version, ConfigDir: a.configDir, BaseURL: a.getenv("WITHHUMAN_CLI_BASE"),
		HTTP: a.http, Stdin: a.stdin, Stdout: a.stdout, Stderr: a.stderr, Env: env,
	})
}

func (a *app) updateNotice(ctx context.Context, args []string) {
	if len(args) == 0 || (args[0] != "agent" && args[0] != "onboard") {
		return
	}
	for _, arg := range args[1:] {
		flag := strings.SplitN(arg, "=", 2)[0]
		if flag == "--json" || flag == "-json" || flag == "--help" || flag == "-help" || flag == "-h" {
			return
		}
	}
	if a.getenv("WITHHUMAN_NO_UPDATE_CHECK") != "1" {
		a.updater().Notice(ctx)
	}
}
