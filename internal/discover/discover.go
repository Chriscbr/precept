// Package discover finds natural-language claims in Go source.
package discover

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Marker identifies the source spelling that introduced a claim.
type Marker string

const (
	MarkerInvariant     Marker = "INVARIANT"
	MarkerPrecondition  Marker = "PRECONDITION"
	MarkerPostcondition Marker = "POSTCONDITION"
	MarkerAssertion     Marker = "ASSERTION"
)

// Claim is a natural-language claim associated with a Go source subject.
type Claim struct {
	Marker     Marker `json:"marker"`
	Text       string `json:"text"`
	Package    string `json:"package"`
	Kind       string `json:"kind"`
	Symbol     string `json:"symbol"`
	File       string `json:"file"`
	MarkerLine int    `json:"marker_line"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
}

// Diagnostic describes a non-fatal issue encountered during discovery.
type Diagnostic struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Result contains all claims and non-fatal diagnostics found by Scan.
type Result struct {
	Claims      []Claim      `json:"claims"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Scan finds INVARIANT, PRECONDITION, POSTCONDITION, and ASSERTION claims in a
// file-or-directory scope using a background context.
func Scan(repoRoot, scope string) (Result, error) {
	return ScanContext(context.Background(), repoRoot, scope)
}

// ScanContext finds claims in a file-or-directory scope. repoRoot and
// scope must be absolute paths, scope must be inside repoRoot, and neither the
// scope path nor any traversed entry may be a symbolic link.
func ScanContext(ctx context.Context, repoRoot, scope string) (Result, error) {
	return scanContextWithWalk(ctx, repoRoot, scope, filepath.WalkDir)
}

type walkDirFunc func(string, fs.WalkDirFunc) error

func scanContextWithWalk(ctx context.Context, repoRoot, scope string, walk walkDirFunc) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("scan context is nil")
	}
	if walk == nil {
		return Result{}, fmt.Errorf("scan walk function is nil")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	repoRoot, scope, err := validatePaths(repoRoot, scope)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Claims:      make([]Claim, 0),
		Diagnostics: make([]Diagnostic, 0),
	}
	err = walk(scope, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}

		fileResult, err := scanFile(ctx, repoRoot, path)
		if err != nil {
			return err
		}
		result.Claims = append(result.Claims, fileResult.Claims...)
		result.Diagnostics = append(result.Diagnostics, fileResult.Diagnostics...)
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return Result{}, fmt.Errorf("scan claims in %q: %w", scope, err)
	}

	sort.Slice(result.Claims, func(i, j int) bool {
		left, right := result.Claims[i], result.Claims[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.MarkerLine != right.MarkerLine {
			return left.MarkerLine < right.MarkerLine
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Symbol != right.Symbol {
			return left.Symbol < right.Symbol
		}
		return left.Marker < right.Marker
	})
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		left, right := result.Diagnostics[i], result.Diagnostics[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		return left.Message < right.Message
	})
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("scan claims in %q: %w", scope, err)
	}

	return result, nil
}

func validatePaths(repoRoot, scope string) (string, string, error) {
	if !filepath.IsAbs(repoRoot) {
		return "", "", fmt.Errorf("repository root must be an absolute path: %q", repoRoot)
	}
	if !filepath.IsAbs(scope) {
		return "", "", fmt.Errorf("scope must be an absolute path: %q", scope)
	}

	repoRoot = filepath.Clean(repoRoot)
	scope = filepath.Clean(scope)
	rel, err := filepath.Rel(repoRoot, scope)
	if err != nil {
		return "", "", fmt.Errorf("resolve scope relative to repository root: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("scope %q is outside repository root %q", scope, repoRoot)
	}

	rootInfo, err := os.Lstat(repoRoot)
	if err != nil {
		return "", "", fmt.Errorf("inspect repository root %q: %w", repoRoot, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("repository root must not be a symbolic link: %q", repoRoot)
	}
	if !rootInfo.IsDir() {
		return "", "", fmt.Errorf("repository root is not a directory: %q", repoRoot)
	}

	current := repoRoot
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				return "", "", fmt.Errorf("inspect scope path %q: %w", current, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", "", fmt.Errorf("scope path must not traverse a symbolic link: %q", current)
			}
		}
	}

	scopeInfo, err := os.Lstat(scope)
	if err != nil {
		return "", "", fmt.Errorf("inspect scope %q: %w", scope, err)
	}
	if !scopeInfo.IsDir() && (!scopeInfo.Mode().IsRegular() || filepath.Ext(scope) != ".go") {
		return "", "", fmt.Errorf("scope is not a directory or supported source file (expected a .go file): %q", scope)
	}
	return repoRoot, scope, nil
}

func scanFile(ctx context.Context, repoRoot, filename string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	rel, err := filepath.Rel(repoRoot, filename)
	if err != nil {
		return Result{}, fmt.Errorf("resolve source path %q relative to repository root: %w", filename, err)
	}
	rel = filepath.ToSlash(rel)

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if contextErr := ctx.Err(); contextErr != nil {
		return Result{}, contextErr
	}
	if parsed != nil && ast.IsGenerated(parsed) {
		return Result{}, nil
	}
	if err != nil {
		return Result{Diagnostics: []Diagnostic{malformedFileDiagnostic(rel, err)}}, nil
	}

	subjects := indexSubjects(fset, parsed)
	var result Result
	for _, group := range parsed.Comments {
		for _, claim := range parseCommentGroup(group) {
			markerLine := sourceLine(fset, claim.comment.Slash)
			if claim.text == "" {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					File:    rel,
					Line:    markerLine,
					Message: fmt.Sprintf("%s claim is empty", claim.marker),
				})
				continue
			}

			subject := subjects.forComment(claim.comment)
			result.Claims = append(result.Claims, Claim{
				Marker:     claim.marker,
				Text:       claim.text,
				Package:    parsed.Name.Name,
				Kind:       subject.kind,
				Symbol:     subject.symbol,
				File:       rel,
				MarkerLine: markerLine,
				StartLine:  sourceLine(fset, subject.start),
				EndLine:    sourceLine(fset, subject.end),
			})
		}
	}

	return result, nil
}

func malformedFileDiagnostic(filename string, parseErr error) Diagnostic {
	diagnostic := Diagnostic{
		File:    filename,
		Message: "skipping malformed Go file: " + parseErr.Error(),
	}
	if parseErrors, ok := parseErr.(scanner.ErrorList); ok && len(parseErrors) > 0 {
		diagnostic.Line = parseErrors[0].Pos.Line
		diagnostic.Message = "skipping malformed Go file: " + parseErrors[0].Msg
		if len(parseErrors) > 1 {
			diagnostic.Message += fmt.Sprintf(" (and %d more parse errors)", len(parseErrors)-1)
		}
	}
	return diagnostic
}

type parsedClaim struct {
	comment *ast.Comment
	marker  Marker
	text    string
}

func parseCommentGroup(group *ast.CommentGroup) []parsedClaim {
	claims := make([]parsedClaim, 0)
	for index, comment := range group.List {
		marker, firstLine, ok := markerText(comment.Text)
		if !ok {
			continue
		}

		lines := make([]string, 0, 1)
		if firstLine != "" {
			lines = append(lines, firstLine)
		}
		for nextIndex := index + 1; nextIndex < len(group.List); nextIndex++ {
			next := group.List[nextIndex]
			if _, _, nextIsMarker := markerText(next.Text); nextIsMarker || resemblesMarker(next.Text) {
				break
			}
			line, isLineComment := lineCommentText(next.Text)
			if !isLineComment || line == "" {
				break
			}
			lines = append(lines, line)
		}

		claims = append(claims, parsedClaim{
			comment: comment,
			marker:  marker,
			text:    strings.Join(lines, "\n"),
		})
	}
	return claims
}

func resemblesMarker(raw string) bool {
	if !strings.HasPrefix(raw, "//") {
		return false
	}
	remainder := trimHorizontalLeft(strings.TrimPrefix(raw, "//"))
	for _, marker := range []string{"INVARIANT", "PRECONDITION", "POSTCONDITION", "ASSERTION"} {
		if len(remainder) < len(marker) || !strings.EqualFold(remainder[:len(marker)], marker) {
			continue
		}
		return strings.HasPrefix(trimHorizontalLeft(remainder[len(marker):]), ":")
	}
	return false
}

func markerText(raw string) (Marker, string, bool) {
	const (
		invariant     = "// INVARIANT:"
		precondition  = "// PRECONDITION:"
		postcondition = "// POSTCONDITION:"
		assertion     = "// ASSERTION:"
	)
	switch {
	case strings.HasPrefix(raw, invariant):
		return MarkerInvariant, trimHorizontal(raw[len(invariant):]), true
	case strings.HasPrefix(raw, precondition):
		return MarkerPrecondition, trimHorizontal(raw[len(precondition):]), true
	case strings.HasPrefix(raw, postcondition):
		return MarkerPostcondition, trimHorizontal(raw[len(postcondition):]), true
	case strings.HasPrefix(raw, assertion):
		return MarkerAssertion, trimHorizontal(raw[len(assertion):]), true
	default:
		return "", "", false
	}
}

func lineCommentText(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "//") {
		return "", false
	}
	return trimHorizontal(strings.TrimPrefix(raw, "//")), true
}

func trimHorizontal(value string) string {
	return strings.Trim(value, " \t")
}

func trimHorizontalLeft(value string) string {
	return strings.TrimLeft(value, " \t")
}

type subject struct {
	kind   string
	symbol string
	start  token.Pos
	end    token.Pos
}

type subjectContainer struct {
	start   token.Pos
	end     token.Pos
	subject subject
}

type subjectIndex struct {
	packageSubject subject
	direct         map[*ast.Comment]subject
	containers     []subjectContainer
}

func indexSubjects(fset *token.FileSet, file *ast.File) subjectIndex {
	packageSubject := subject{
		kind:   "package",
		symbol: file.Name.Name,
		start:  file.Package,
		end:    file.Name.End(),
	}
	index := subjectIndex{
		packageSubject: packageSubject,
		direct:         make(map[*ast.Comment]subject),
	}
	index.register(file.Doc, packageSubject)

	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			function := functionSubject(fset, declaration)
			index.register(declaration.Doc, function)
			if declaration.Body != nil {
				index.containers = append(index.containers, subjectContainer{
					start:   declaration.Body.Pos(),
					end:     declaration.Body.End(),
					subject: function,
				})
			}
		case *ast.GenDecl:
			indexGeneralDeclaration(fset, &index, declaration)
		}
	}

	return index
}

func indexGeneralDeclaration(fset *token.FileSet, index *subjectIndex, declaration *ast.GenDecl) {
	for _, specification := range declaration.Specs {
		switch specification := specification.(type) {
		case *ast.TypeSpec:
			if declaration.Tok != token.TYPE {
				continue
			}
			typeSubject := subject{
				kind:   "type",
				symbol: specification.Name.Name,
				start:  specification.Pos(),
				end:    specification.End(),
			}
			index.register(specification.Doc, typeSubject)
			index.register(specification.Comment, typeSubject)
			if specification.Doc == nil && !declaration.Lparen.IsValid() && len(declaration.Specs) == 1 {
				index.register(declaration.Doc, typeSubject)
			}
			index.containers = append(index.containers, subjectContainer{
				start:   specification.Type.Pos(),
				end:     specification.Type.End(),
				subject: typeSubject,
			})
			if structure, ok := specification.Type.(*ast.StructType); ok {
				indexStructFields(fset, index, specification.Name.Name, structure)
			}
		case *ast.ValueSpec:
			if declaration.Tok != token.CONST && declaration.Tok != token.VAR {
				continue
			}
			valueSubject := subject{
				kind:   strings.ToLower(declaration.Tok.String()),
				symbol: namesSymbol(specification.Names),
				start:  specification.Pos(),
				end:    specification.End(),
			}
			index.register(specification.Doc, valueSubject)
			index.register(specification.Comment, valueSubject)
			if specification.Doc == nil && !declaration.Lparen.IsValid() && len(declaration.Specs) == 1 {
				index.register(declaration.Doc, valueSubject)
			}
		}
	}
}

func indexStructFields(fset *token.FileSet, index *subjectIndex, typeName string, structure *ast.StructType) {
	for _, field := range structure.Fields.List {
		fieldName := namesSymbol(field.Names)
		if fieldName == "" {
			fieldName = embeddedFieldName(field.Type)
		}
		if fieldName == "" {
			fieldName = formattedNode(fset, field.Type)
		}
		fieldSubject := subject{
			kind:   "field",
			symbol: typeName + "." + fieldName,
			start:  field.Pos(),
			end:    field.End(),
		}
		index.register(field.Doc, fieldSubject)
		index.register(field.Comment, fieldSubject)
	}
}

func functionSubject(fset *token.FileSet, declaration *ast.FuncDecl) subject {
	kind := "func"
	symbol := declaration.Name.Name
	if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
		kind = "method"
		symbol = methodSymbol(fset, declaration)
	}
	return subject{
		kind:   kind,
		symbol: symbol,
		start:  declaration.Pos(),
		end:    declaration.End(),
	}
}

func (index *subjectIndex) register(group *ast.CommentGroup, value subject) {
	if group == nil {
		return
	}
	for _, comment := range group.List {
		index.direct[comment] = value
	}
}

func (index subjectIndex) forComment(comment *ast.Comment) subject {
	if value, ok := index.direct[comment]; ok {
		return value
	}

	var best *subjectContainer
	for containerIndex := range index.containers {
		candidate := &index.containers[containerIndex]
		if comment.Pos() < candidate.start || comment.End() > candidate.end {
			continue
		}
		if best == nil || candidate.end-candidate.start < best.end-best.start {
			best = candidate
		}
	}
	if best != nil {
		return best.subject
	}
	return index.packageSubject
}

func namesSymbol(names []*ast.Ident) string {
	if len(names) == 0 {
		return ""
	}
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, name.Name)
	}
	if len(values) == 1 {
		return values[0]
	}
	return "{" + strings.Join(values, ",") + "}"
}

func embeddedFieldName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		return expression.Sel.Name
	case *ast.StarExpr:
		return embeddedFieldName(expression.X)
	case *ast.IndexExpr:
		return embeddedFieldName(expression.X)
	case *ast.IndexListExpr:
		return embeddedFieldName(expression.X)
	case *ast.ParenExpr:
		return embeddedFieldName(expression.X)
	default:
		return ""
	}
}

func formattedNode(fset *token.FileSet, node ast.Node) string {
	var output bytes.Buffer
	if err := format.Node(&output, fset, node); err != nil {
		return "<anonymous>"
	}
	return output.String()
}

func sourceLine(fset *token.FileSet, position token.Pos) int {
	return fset.PositionFor(position, false).Line
}

func methodSymbol(fset *token.FileSet, declaration *ast.FuncDecl) string {
	receiverName := formattedNode(fset, declaration.Recv.List[0].Type)
	if strings.HasPrefix(receiverName, "*") {
		receiverName = "(" + receiverName + ")"
	}
	return receiverName + "." + declaration.Name.Name
}
