package usagesource

import (
	"os"
	"runtime"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
)

func setTempRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv("TMPDIR", root)
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", root)
		t.Setenv("TEMP", root)
	}
}

func assertTempRootEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", root, err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Errorf("temp root %q contains %v, want empty", root, names)
	}
}

// TestSourcesConformToTheRegistryContract enumerates every source Sources
// holds and proves each keeps the promise the registry's callers rely on:
// its own kind is a name it recognizes, it never arms a remote launch, it
// arms an assignment for a local launch only where it can identify the
// runtime, and Close leaves no trace, retried or not.
func TestSourcesConformToTheRegistryContract(t *testing.T) {
	kinds := Sources.Kinds()
	if len(kinds) == 0 {
		t.Fatal("Sources.Kinds() = empty, want at least one registered source")
	}

	for _, kind := range kinds {
		root := t.TempDir()
		setTempRoot(t, root)

		constructor, err := Sources.Get(kind)
		if err != nil {
			t.Fatalf("Sources.Get(%q) error = %v", kind, err)
		}

		if fresh := constructor(); !fresh.Recognize(kind) {
			t.Errorf("Recognize(%q) = false, want true: a source's own kind is a name it recognizes", kind)
		}

		remote := agentcore.LaunchTarget{Command: "ssh", RemoteCommand: "agent", SSHHost: "worker"}
		for _, offeredRuntime := range []string{"", kind} {
			refused := constructor()
			if assignments, claimed := refused.Claim(remote, offeredRuntime); claimed {
				t.Errorf("Claim(remote, %q) = %v, true, want a refusal: this source reads a local filesystem", offeredRuntime, assignments)
			}
			assertTempRootEmpty(t, root)
			refused.Close()
			assertTempRootEmpty(t, root)
		}

		neutral := agentcore.LaunchTarget{
			Command:       "/opt/sortie-neutral/sortie-neutral-launch",
			Args:          []string{"--acp"},
			WorkspacePath: t.TempDir(),
		}

		firstOffer := constructor()
		if assignments, claimed := firstOffer.Claim(neutral, ""); claimed && len(assignments) != 0 {
			t.Errorf("Claim(neutral, \"\") claimed with assignments %v, want no assignment: this source cannot identify the neutral launch",
				assignments)
		}
		assertTempRootEmpty(t, root)
		firstOffer.Close()
		assertTempRootEmpty(t, root)

		relaunch := constructor()
		if _, claimed := relaunch.Claim(neutral, kind); !claimed {
			t.Fatalf("Claim(neutral, %q) = false, want true: the relaunch offer claims exactly when Recognize accepts", kind)
		}
		relaunch.Close()
		assertTempRootEmpty(t, root)
		relaunch.Close()
		assertTempRootEmpty(t, root)

		untouched := constructor()
		untouched.Close()
		assertTempRootEmpty(t, root)
	}
}
