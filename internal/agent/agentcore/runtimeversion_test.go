package agentcore

import "testing"

func TestParseRuntimeVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		out         string
		wantVersion string
		wantMajor   int
		wantOK      bool
	}{
		{name: "1.x bare version", out: "1.18.32\n", wantVersion: "1.18.32", wantMajor: 1, wantOK: true},
		{name: "2.x opencode bin, v-prefixed", out: "opencode v2.0.18\n", wantVersion: "2.0.18", wantMajor: 2, wantOK: true},
		{name: "2.x opencode2 bin, v-prefixed", out: "opencode2 v2.0.18\n", wantVersion: "2.0.18", wantMajor: 2, wantOK: true},
		{name: "2.x prerelease suffix", out: "opencode v2.1.0-beta.3\n", wantVersion: "2.1.0-beta.3", wantMajor: 2, wantOK: true},
		{name: "2.x trailing build metadata token is ignored", out: "opencode 2.0.18 (abc123)\n", wantVersion: "2.0.18", wantMajor: 2, wantOK: true},
		{name: "leading blank lines are skipped", out: "\n\n1.19.0\n", wantVersion: "1.19.0", wantMajor: 1, wantOK: true},
		{name: "an unsupported major still parses", out: "0.0.0-dev-202609250101\n", wantVersion: "0.0.0-dev-202609250101", wantMajor: 0, wantOK: true},
		{name: "major 3 still parses", out: "3.0.0\n", wantVersion: "3.0.0", wantMajor: 3, wantOK: true},
		{name: "empty output is not ok", out: "", wantOK: false},
		{name: "usage text carries no version", out: "Usage: opencode [options]\n", wantOK: false},
		{name: "a two-component number is not a bare semantic version", out: "1.18\n", wantOK: false},
		{name: "a trailing sentence period is dropped", out: "GitHub Copilot CLI 1.0.103.\nRun 'copilot update' to check for updates.\n", wantVersion: "1.0.103", wantMajor: 1, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			version, major, ok := ParseRuntimeVersion([]byte(tt.out))

			if ok != tt.wantOK {
				t.Fatalf("ParseRuntimeVersion(%q) ok = %v, want %v", tt.out, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if version != tt.wantVersion {
				t.Errorf("ParseRuntimeVersion(%q) version = %q, want %q", tt.out, version, tt.wantVersion)
			}
			if major != tt.wantMajor {
				t.Errorf("ParseRuntimeVersion(%q) major = %d, want %d", tt.out, major, tt.wantMajor)
			}
		})
	}
}
