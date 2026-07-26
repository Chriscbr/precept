package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type resolvedScope struct {
	repositoryRoot string
	absolutePath   string
	relativePath   string
}

func resolveScope(ctx context.Context, path string) (resolvedScope, error) {
	if ctx == nil {
		return resolvedScope{}, fmt.Errorf("resolve scope context is nil")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return resolvedScope{}, fmt.Errorf("resolve scope %q: %w", path, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return resolvedScope{}, fmt.Errorf("inspect scope %q: %w", path, err)
	}
	if !info.IsDir() && (!info.Mode().IsRegular() || filepath.Ext(absPath) != ".go") {
		return resolvedScope{}, fmt.Errorf("scope %q is not a directory or supported source file (expected a .go file)", path)
	}

	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return resolvedScope{}, fmt.Errorf("resolve scope symlinks %q: %w", path, err)
	}

	gitDirectory := resolvedPath
	if !info.IsDir() {
		gitDirectory = filepath.Dir(resolvedPath)
	}
	git := exec.CommandContext(ctx, "git", "-C", gitDirectory, "rev-parse", "--show-toplevel")
	output, err := git.CombinedOutput()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return resolvedScope{}, fmt.Errorf("find Git repository for %q: %w", path, contextErr)
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return resolvedScope{}, fmt.Errorf("find Git repository for %q: %s", path, message)
	}

	repositoryRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil {
		return resolvedScope{}, fmt.Errorf("resolve repository root: %w", err)
	}
	relativePath, err := filepath.Rel(repositoryRoot, resolvedPath)
	if err != nil {
		return resolvedScope{}, fmt.Errorf("make scope repository-relative: %w", err)
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath) {
		return resolvedScope{}, fmt.Errorf("scope %q resolves outside repository %q", path, repositoryRoot)
	}

	return resolvedScope{
		repositoryRoot: repositoryRoot,
		absolutePath:   resolvedPath,
		relativePath:   filepath.ToSlash(relativePath),
	}, nil
}
