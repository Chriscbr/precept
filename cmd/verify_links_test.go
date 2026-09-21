package cmd

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryWebURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ remote, want string }{
		{"git@github.com:org/repo.git", "https://github.com/org/repo"},
		{"github.com:org/repo.git", "https://github.com/org/repo"},
		{"ssh://git@github.com:22/org/repo.git", "https://github.com/org/repo"},
		{"ssh://git@ssh.github.com:443/org/repo.git", "https://github.com/org/repo"},
		{"https://token:secret@github.com/org/repo.git/", "https://github.com/org/repo"},
		{"https://github.company.test:8443/org/repo", "https://github.company.test:8443/org/repo"},
		{"git@github.company.test:org/repo.git", "https://github.company.test/org/repo"},
		{"git://github.com/org/repo.git", "https://github.com/org/repo"},
		{"/tmp/repo", ""},
		{"file:///tmp/repo", ""},
		{"javascript://github.com/org/repo", ""},
		{"https://github.com/org/repo?token=secret", ""},
		{"https://github.com/org/repo#fragment", ""},
		{"https://github.com/org", ""},
		{"https://github.com/../repo", ""},
		{"https://github.com/org/repo%zz", ""},
	} {
		t.Run(test.remote, func(t *testing.T) {
			got := ""
			if parsed := repositoryWebURL(test.remote); parsed != nil {
				got = parsed.String()
			}
			if got != test.want {
				t.Fatalf("repositoryWebURL(%q) = %q, want %q", test.remote, got, test.want)
			}
		})
	}
}

func TestSourceURLsOnlyLinkUnchangedCommittedFiles(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("remote", "add", "origin", "git@github.com:example/repo.git")
	if got := sourceURLs(context.Background(), repository); len(got) != 0 {
		t.Fatalf("unborn HEAD produced links: %v", got)
	}
	for _, file := range []string{"clean.go", "staged.go", "dirty.go", "a #|).go"} {
		mustWriteFile(t, filepath.Join(repository, file), "package fixture\n", 0o644)
	}
	git("add", ".")
	git("-c", "user.name=Precept Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	revision := git("rev-parse", "HEAD")
	mustWriteFile(t, filepath.Join(repository, "staged.go"), "package changed\n", 0o644)
	git("add", "staged.go")
	mustWriteFile(t, filepath.Join(repository, "dirty.go"), "package changed\n", 0o644)
	mustWriteFile(t, filepath.Join(repository, "untracked.go"), "package fixture\n", 0o644)
	mustWriteFile(t, filepath.Join(repository, "added.go"), "package fixture\n", 0o644)
	git("add", "added.go")
	links := sourceURLs(context.Background(), repository)
	if len(links) != 2 || links["clean.go"] != "https://github.com/example/repo/blob/"+revision+"/clean.go" {
		t.Fatalf("links = %v, want only clean committed files", links)
	}
	if !strings.HasSuffix(links["a #|).go"], "/a%20%23%7C%29.go") && !strings.HasSuffix(links["a #|).go"], "/a%20%23%7C).go") {
		t.Fatalf("filename was not URL encoded: %q", links["a #|).go"])
	}
	git("remote", "remove", "origin")
	if got := sourceURLs(context.Background(), repository); len(got) != 0 {
		t.Fatalf("missing remote produced links: %v", got)
	}
}
