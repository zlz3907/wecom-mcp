package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Invoke the actual argument parser in a subprocess because main uses os.Exit.
func TestStaticRecoveryVerifierCLIHelper(t *testing.T) {
	if os.Getenv("TEST_STATIC_RECOVERY_CLI_HELPER") != "1" {
		return
	}
	os.Args = append([]string{"wecom-mcp-team"}, os.Args[3:]...)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestStaticRecoveryVerifierCLIRejectsIncompleteMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"explicit_empty", []string{"--gnas-static-only="}},
		{"missing_hybrid", []string{"--gnas-static-only=https://a.example"}},
		{"missing_mapping", []string{"--gnas-static-only=https://a.example", "--gnas-discovery-policy=/fake/policy.json", "--gnas-state-root=/fake/state"}},
		{"missing_policy", []string{"--gnas-static-only=https://a.example", "--gnas-fleet-runtime=/fake/runtime.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestStaticRecoveryVerifierCLIHelper$", "--"}, tc.args...)...)
			cmd.Env = append(os.Environ(), "TEST_STATIC_RECOVERY_CLI_HELPER=1")
			output, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 2 {
				t.Fatalf("configuration must exit 2; err=%v output=%s", err, output)
			}
			if !strings.Contains(string(output), "static recovery") {
				t.Fatalf("static recovery mode not recognized: %s", output)
			}
		})
	}
}
