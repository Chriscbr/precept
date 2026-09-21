package cmd

import (
	"context"
	"net/url"
	"os/exec"
	"strings"
)

// sourceURLs uses local Git metadata only. Missing metadata is not a verification
// failure. Files absent from HEAD or changed in the index/worktree have no link,
// because a commit permalink would point reviewers at different source lines.
func sourceURLs(ctx context.Context, repository string) map[string]string {
	git := func(arguments ...string) (string, error) {
		output, err := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, arguments...)...).Output()
		return string(output), err
	}
	remote, err := git("config", "--get", "remote.origin.url")
	if err != nil {
		return nil
	}
	base := repositoryWebURL(strings.TrimSpace(remote))
	if base == nil {
		return nil
	}
	revision, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil
	}
	files, err := git("ls-tree", "-r", "--name-only", "-z", "HEAD")
	if err != nil {
		return nil
	}
	changed, err := git("diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return nil
	}
	dirty := make(map[string]bool)
	for _, file := range strings.Split(changed, "\x00") {
		dirty[file] = true
	}
	links := make(map[string]string)
	for _, file := range strings.Split(files, "\x00") {
		if file == "" || dirty[file] {
			continue
		}
		target := *base
		target.Path += "/blob/" + strings.TrimSpace(revision) + "/" + file
		links[file] = target.String()
	}
	return links
}

// repositoryWebURL normalizes HTTPS, SSH, and scp-style GitHub-compatible
// remotes, including GitHub Enterprise. Credentials and SSH ports are omitted.
// Local paths and remotes without an owner/repository pair have no web URL.
func repositoryWebURL(remote string) *url.URL {
	if !strings.Contains(remote, "://") {
		host, path, ok := strings.Cut(remote, ":")
		if !ok || strings.ContainsAny(host, "/\\") {
			return nil
		}
		if _, after, found := strings.Cut(host, "@"); found {
			host = after
		}
		if !strings.Contains(host, ".") {
			return nil
		}
		remote = "ssh://" + host + "/" + path
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil
	}
	switch parsed.Scheme {
	case "http", "https":
	case "ssh", "git":
		parsed.Scheme = "https"
		parsed.Host = parsed.Hostname()
		if parsed.Host == "ssh.github.com" {
			parsed.Host = "github.com"
		}
	default:
		return nil
	}
	path := strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), ".git")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return nil
	}
	parsed.Path, parsed.RawPath, parsed.User = path, "", nil
	return parsed
}
