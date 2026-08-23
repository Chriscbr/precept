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

type scopeResolver struct {
	ctx            context.Context
	argument       string
	absolutePath   string
	resolvedPath   string
	isDirectory    bool
	repositoryRoot string
	relativePath   string
	err            error
}

func resolveScope(ctx context.Context, path string) (resolvedScope, error) {
	resolver := &scopeResolver{ctx: ctx, argument: path}
	resolver.validateContext()
	resolver.makeAbsolute()
	resolver.inspectPath()
	resolver.resolvePathSymlinks()
	resolver.findRepository()
	resolver.resolveRepositorySymlinks()
	resolver.makeRelative()
	resolver.ensureInsideRepository()
	return resolver.result(), resolver.err
}

func (resolver *scopeResolver) validateContext() {
	if resolver.ctx == nil {
		resolver.err = fmt.Errorf("resolve scope context is nil")
	}
}

func (resolver *scopeResolver) makeAbsolute() {
	if resolver.err != nil {
		return
	}
	resolver.absolutePath, resolver.err = filepath.Abs(resolver.argument)
	if resolver.err != nil {
		resolver.err = fmt.Errorf("resolve scope %q: %w", resolver.argument, resolver.err)
	}
}

func (resolver *scopeResolver) inspectPath() {
	if resolver.err != nil {
		return
	}
	info, err := os.Stat(resolver.absolutePath)
	if err != nil {
		resolver.err = fmt.Errorf("inspect scope %q: %w", resolver.argument, err)
		return
	}
	resolver.isDirectory = info.IsDir()
	if !resolver.isDirectory && (!info.Mode().IsRegular() || filepath.Ext(resolver.absolutePath) != ".go") {
		resolver.err = fmt.Errorf("scope %q is not a directory or supported source file (expected a .go file)", resolver.argument)
	}
}

func (resolver *scopeResolver) resolvePathSymlinks() {
	if resolver.err != nil {
		return
	}
	resolver.resolvedPath, resolver.err = filepath.EvalSymlinks(resolver.absolutePath)
	if resolver.err != nil {
		resolver.err = fmt.Errorf("resolve scope symlinks %q: %w", resolver.argument, resolver.err)
	}
}

func (resolver *scopeResolver) findRepository() {
	if resolver.err != nil {
		return
	}
	gitDirectory := resolver.resolvedPath
	if !resolver.isDirectory {
		gitDirectory = filepath.Dir(resolver.resolvedPath)
	}
	command := exec.CommandContext(resolver.ctx, "git", "-C", gitDirectory, "rev-parse", "--show-toplevel")
	output, err := command.CombinedOutput()
	if err == nil {
		resolver.repositoryRoot = strings.TrimSpace(string(output))
		return
	}
	resolver.err = resolver.repositoryError(err, output)
}

func (resolver *scopeResolver) repositoryError(commandErr error, output []byte) error {
	if contextErr := resolver.ctx.Err(); contextErr != nil {
		return fmt.Errorf("find Git repository for %q: %w", resolver.argument, contextErr)
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = commandErr.Error()
	}
	return fmt.Errorf("find Git repository for %q: %s", resolver.argument, message)
}

func (resolver *scopeResolver) resolveRepositorySymlinks() {
	if resolver.err != nil {
		return
	}
	resolver.repositoryRoot, resolver.err = filepath.EvalSymlinks(resolver.repositoryRoot)
	if resolver.err != nil {
		resolver.err = fmt.Errorf("resolve repository root: %w", resolver.err)
	}
}

func (resolver *scopeResolver) makeRelative() {
	if resolver.err != nil {
		return
	}
	resolver.relativePath, resolver.err = filepath.Rel(resolver.repositoryRoot, resolver.resolvedPath)
	if resolver.err != nil {
		resolver.err = fmt.Errorf("make scope repository-relative: %w", resolver.err)
	}
}

func (resolver *scopeResolver) ensureInsideRepository() {
	if resolver.err != nil {
		return
	}
	outside := resolver.relativePath == ".." ||
		strings.HasPrefix(resolver.relativePath, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(resolver.relativePath)
	if outside {
		resolver.err = fmt.Errorf("scope %q resolves outside repository %q", resolver.argument, resolver.repositoryRoot)
	}
}

func (resolver *scopeResolver) result() resolvedScope {
	return resolvedScope{
		repositoryRoot: resolver.repositoryRoot,
		absolutePath:   resolver.resolvedPath,
		relativePath:   filepath.ToSlash(resolver.relativePath),
	}
}
