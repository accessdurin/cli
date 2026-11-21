package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/ports"
)

// observerAnswer answers setup's agent questions without a checker or reporting, and the
// observer question with install.
type observerAnswer struct {
	ports.SetupAgentQuestions
	install bool
	asked   int
	result  ports.IntegrationsResult
}

func (*observerAnswer) Checker(context.Context, contracts.AgentConfig) (*contracts.HookChecker, error) {
	return nil, nil
}

func (*observerAnswer) Reporting(context.Context, contracts.AgentConfig, bool) (bool, error) {
	return false, nil
}

func (a *observerAnswer) Observer(context.Context) (bool, error) {
	a.asked++
	return a.install, nil
}

func (a *observerAnswer) Integrations(result ports.IntegrationsResult) { a.result = result }

// Setup installs capture hooks either way, but the observer service, which keeps full hook
// payloads, only when the user agrees; the terminal question defaults to no.
func TestSetupInstallsObserverOnlyWithConsent(t *testing.T) {
	original := installObserverService
	t.Cleanup(func() { installObserverService = original })
	for _, install := range []bool{false, true} {
		root := tempDir(t)
		installed := 0
		installObserverService = func(context.Context, string, string) string {
			installed++
			return "installed as a test service"
		}
		answers := &observerAnswer{install: install}
		setup := &setupContext{SetupOptions{Directory: filepath.Join(root, "durin"), UI: ports.SetupUi{Agent: answers, Stage: func(string) {}}}}
		bridge := BridgeConfiguration{ExecutablePath: filepath.Join(root, "runtime", "durin"), ConfigDirectory: filepath.Join(root, "durin"), ProfileID: reportProfile}
		if err := setup.setupIntegrations(context.Background(), AgentsFor(root, EnvMap{})["claude"], bridge); err != nil {
			t.Fatal(err)
		}
		want, observer := 0, "not installed; turn it on later with durin observer install"
		if install {
			want, observer = 1, "installed as a test service"
		}
		if answers.asked != 1 || installed != want || !answers.result.Hooks || answers.result.Observer != observer {
			t.Errorf("install=%v: asked %d, installed %d, result %+v", install, answers.asked, installed, answers.result)
		}
	}

	var out bytes.Buffer
	install, err := agentQuestions{&terminalUI{p: newPrompter(strings.NewReader("\r"), &out)}}.Observer(context.Background())
	if err != nil || install {
		t.Fatalf("pressing enter installs the observer: %v %v", install, err)
	}
	for _, want := range []string{"prompts, tool inputs and outputs", "up to 7 days or 512 MiB", "Install the Durin observer?"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
