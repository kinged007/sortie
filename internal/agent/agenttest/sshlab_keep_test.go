package agenttest

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
)

func TestSSHLabKeep(t *testing.T) {
	if os.Getenv("SORTIE_SSHLAB_KEEP") != "1" {
		t.Skip("skipping manual SSH lab: set SORTIE_SSHLAB_KEEP=1 to enable")
	}
	if reason := SSHLabSkipReason(); reason != "" {
		t.Skip(reason)
	}

	lab := StartSSHLab(t, SSHLabParams{Install: os.Getenv("SORTIE_SSHLAB_INSTALL")})

	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("exec.LookPath(%q) = %v, want nil", "ssh", err)
	}
	t.Logf("connect: %s %s", ssh, lab.Host)
	t.Logf("host that refuses TCP forwarding: %s", lab.RefusedHost)
	t.Logf("shell in the container: docker exec -it %s sh", lab.container)

	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}
