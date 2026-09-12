package server

import (
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/client"
	"github.com/g-imhoff/yodea/internal/sites"
)

// TestProjectNameContractAgrees pins the client/server validation contract:
// client.CheckProject, sites.CheckProjectName, and validProject must agree
// on every name. The single source of truth is sites.CheckProjectName.
func TestProjectNameContractAgrees(t *testing.T) {
	names := []string{
		"",
		"   ", "\t", "\n", " - ",
		".", "..", "...",
		"-bad", "bad-", "-", "--", "-a", "a-",
		strings.Repeat("a", 40),
		strings.Repeat("a", 41),
		strings.Repeat("p", 80),
		"---", "!!!", "___", "??", "Site", "SITE", "site!",
		"site", "blog", "my-app", "a1", "my.project",
		"caf\u00e9", "\u65e5\u672c\u8a9e", "na\u00efve",
		"a\x00b", "a\nb", "a\tb", "\x00",
		"a/b", `a\b`, "a?b", "a#b",
	}
	for _, raw := range names {
		t.Run("name/"+raw, func(t *testing.T) {
			clientErr := client.CheckProject(raw)
			sitesErr := sites.CheckProjectName(raw)
			serverOK := validProject(raw)
			if (clientErr == nil) != serverOK {
				t.Errorf("contract drift: client.CheckProject(%q) err=%v, validProject=%v", raw, clientErr, serverOK)
			}
			if (sitesErr == nil) != serverOK {
				t.Errorf("contract drift: sites.CheckProjectName(%q) err=%v, validProject=%v", raw, sitesErr, serverOK)
			}
		})
	}
}

// TestUploadLimitContract pins the deploy-size contract: the CLI limit must
// stay identical to the server's sites.MaxUploadBytes.
func TestUploadLimitContract(t *testing.T) {
	if client.MaxUploadBytes != sites.MaxUploadBytes {
		t.Fatalf("contract drift: client.MaxUploadBytes=%d, sites.MaxUploadBytes=%d",
			client.MaxUploadBytes, sites.MaxUploadBytes)
	}
}
