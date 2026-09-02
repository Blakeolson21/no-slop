package agent

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// InvocationIdentity is observed launch identity for one concrete agent
// adapter. ModelArgs contains only model/provider-selecting argv entries; it
// never contains the prompt, environment, or unrelated arguments.
type InvocationIdentity struct {
	ConfiguredAgent string
	Executable      *string
	ModelArgs       []string
}

// InvocationIdentityReporter is implemented by executable-backed adapters and
// by decorators that can forward the underlying adapter's identity.
type InvocationIdentityReporter interface {
	InvocationIdentity() InvocationIdentity
}

// ResolveInvocationIdentity returns the launch identity an adapter can observe.
// Missing capabilities stay unknown rather than being guessed from Agent.Name.
func ResolveInvocationIdentity(a Agent) InvocationIdentity {
	if reporter, ok := a.(InvocationIdentityReporter); ok {
		return reporter.InvocationIdentity()
	}
	return InvocationIdentity{}
}

func nativeInvocationIdentity(configuredAgent, bin string, args []string) InvocationIdentity {
	identity := InvocationIdentity{ConfiguredAgent: configuredAgent, ModelArgs: modelSelectingArgs(args)}
	path, err := exec.LookPath(bin)
	if err != nil {
		return identity
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return identity
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return identity
	}
	path = filepath.Clean(path)
	identity.Executable = &path
	return identity
}

// modelSelectingArgs retains only argv entries whose documented purpose is
// selecting a model or provider. Flags with separate values keep that value;
// no other argument is persisted, so credentials and prompts cannot leak into
// invocation telemetry.
func modelSelectingArgs(args []string) []string {
	selected := make([]string, 0)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-m" || arg == "--model" || arg == "--provider" || arg == "--model-provider":
			selected = append(selected, arg)
			if i+1 < len(args) {
				selected = append(selected, args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "--model=") || strings.HasPrefix(arg, "-m=") ||
			strings.HasPrefix(arg, "--provider=") || strings.HasPrefix(arg, "--model-provider="):
			selected = append(selected, arg)
		case (arg == "-c" || arg == "--config") && i+1 < len(args) && strings.HasPrefix(args[i+1], "model="):
			selected = append(selected, arg, args[i+1])
			i++
		}
	}
	return selected
}

func withInvocationIdentity(opts RunOpts, a Agent) RunOpts {
	identity := ResolveInvocationIdentity(a)
	opts.invocationIdentity = &identity
	return opts
}

func (a *claudeAgent) InvocationIdentity() InvocationIdentity {
	return nativeInvocationIdentity("claude", a.bin, a.extraArgs)
}

func (a *codexAgent) InvocationIdentity() InvocationIdentity {
	return nativeInvocationIdentity("codex", a.bin, a.extraArgs)
}

func (a *rovodevAgent) InvocationIdentity() InvocationIdentity {
	return nativeInvocationIdentity("rovodev", a.bin, a.extraArgs)
}

func (a *opencodeAgent) InvocationIdentity() InvocationIdentity {
	return nativeInvocationIdentity("opencode", a.bin, a.extraArgs)
}

func (a *piAgent) InvocationIdentity() InvocationIdentity {
	return nativeInvocationIdentity("pi", a.bin, a.extraArgs)
}

func (a *copilotAgent) InvocationIdentity() InvocationIdentity {
	return nativeInvocationIdentity("copilot", a.bin, a.extraArgs)
}

func (a *acpxAgent) InvocationIdentity() InvocationIdentity {
	identity := nativeInvocationIdentity(a.configuredAgent, a.bin, nil)
	// The model may live inside rawCommand, which is an opaque shell command and
	// may contain credentials. Do not persist or guess any selection args from it.
	identity.ModelArgs = nil
	return identity
}
