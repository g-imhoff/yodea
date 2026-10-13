package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/help/*.golden from the current help output")

// golden returns testdata/help/<topic>.golden.
func golden(t *testing.T, topic string) string {
	t.Helper()
	b, err := os.ReadFile(goldenPath(topic))
	if err != nil {
		t.Fatalf("%v (run go test -run TestHelpGolden -update to create it)", err)
	}
	return string(b)
}

func goldenPath(topic string) string {
	return filepath.Join("testdata", "help", topic+".golden")
}

// TestHelpGolden checks every help path against testdata/help: the
// overview and each command's help, with and without a leading --server,
// on stdout for help, -h, -help and --help, and on stderr for --h, which
// only the flag package treats as help.
func TestHelpGolden(t *testing.T) {
	t.Parallel()
	type call struct {
		args     []string
		onStderr bool
	}
	topics := map[string][]call{
		"overview": {{args: []string{"help"}}, {args: []string{"-h"}}, {args: []string{"--server", "http://127.0.0.1:8093", "--help"}}},
	}
	for _, name := range commandNames() {
		topics[name] = []call{
			{args: []string{"help", name}},
			{args: []string{name, "--help"}},
			{args: []string{name, "-h"}},
			{args: []string{"--server", "http://127.0.0.1:8093", name, "--help"}},
			{args: []string{"--server=http://127.0.0.1:8093", name, "-help"}},
			{args: []string{name, "--h"}, onStderr: true},
		}
	}
	for topic, calls := range topics {
		path := goldenPath(topic)
		if *update {
			out, _, err := runCLI(t, nil, calls[0].args...)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want := golden(t, topic)
		for _, c := range calls {
			stdout, stderr, err := runCLI(t, nil, c.args...)
			if err != nil {
				t.Errorf("yodea %s: %v", strings.Join(c.args, " "), err)
				continue
			}
			got, other := stdout, stderr
			if c.onStderr {
				got, other = stderr, stdout
			}
			if got != want {
				t.Errorf("yodea %s printed\n%s\nwant (%s)\n%s", strings.Join(c.args, " "), got, path, want)
			}
			if other != "" {
				t.Errorf("yodea %s also printed %q on the other stream", strings.Join(c.args, " "), other)
			}
		}
	}
}

// TestOverviewShowsSynopsis checks that each command's overview entry,
// which is aligned by hand, starts with the synopsis its help prints.
func TestOverviewShowsSynopsis(t *testing.T) {
	t.Parallel()
	for _, c := range commands {
		if !strings.HasPrefix(c.synopsis+" ", c.name+" ") {
			t.Errorf("%s: synopsis %q does not start with the command name", c.name, c.synopsis)
		}
		if len(c.summary) == 0 || !strings.HasPrefix(c.summary[0]+" ", c.synopsis+" ") {
			t.Errorf("%s: overview entry %q does not start with the synopsis %q", c.name, c.summary, c.synopsis)
		}
	}
}
