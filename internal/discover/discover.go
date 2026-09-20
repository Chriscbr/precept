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
	"unicode"
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
	ID         string `json:"id"`
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
	execution := &scanExecution{
		ctx:      ctx,
		repoRoot: repoRoot,
		scope:    scope,
		walk:     walk,
		result: Result{
			Claims:      make([]Claim, 0),
			Diagnostics: make([]Diagnostic, 0),
		},
	}
	execution.validateInputs()
	execution.resolvePaths()
	execution.walkScope()
	execution.sortResult()
	execution.checkContext()
	return execution.result, execution.err
}

type scanExecution struct {
	ctx      context.Context
	repoRoot string
	scope    string
	walk     walkDirFunc
	result   Result
	err      error
}

func (execution *scanExecution) validateInputs() {
	switch {
	case execution.ctx == nil:
		execution.err = fmt.Errorf("scan context is nil")
	case execution.walk == nil:
		execution.err = fmt.Errorf("scan walk function is nil")
	default:
		execution.err = execution.ctx.Err()
	}
}

func (execution *scanExecution) resolvePaths() {
	if execution.err != nil {
		return
	}
	execution.repoRoot, execution.scope, execution.err = validatePaths(execution.repoRoot, execution.scope)
}

func (execution *scanExecution) walkScope() {
	if execution.err != nil {
		return
	}
	execution.err = execution.walk(execution.scope, execution.visit)
	if execution.err != nil {
		execution.result = Result{}
		execution.err = fmt.Errorf("scan claims in %q: %w", execution.scope, execution.err)
	}
}

func (execution *scanExecution) visit(path string, entry fs.DirEntry, walkErr error) error {
	if err := execution.ctx.Err(); err != nil {
		return err
	}
	if walkErr != nil {
		return walkErr
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return skipSymbolicLink(entry)
	}
	if entry.IsDir() || filepath.Ext(path) != ".go" {
		return nil
	}
	fileResult, err := scanFile(execution.ctx, execution.repoRoot, path)
	if err == nil {
		execution.result.Claims = append(execution.result.Claims, fileResult.Claims...)
		execution.result.Diagnostics = append(execution.result.Diagnostics, fileResult.Diagnostics...)
	}
	return err
}

func skipSymbolicLink(entry fs.DirEntry) error {
	if entry.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func (execution *scanExecution) sortResult() {
	if execution.err != nil {
		return
	}
	sort.Slice(execution.result.Claims, func(i, j int) bool {
		left, right := execution.result.Claims[i], execution.result.Claims[j]
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
	sort.Slice(execution.result.Diagnostics, func(i, j int) bool {
		left, right := execution.result.Diagnostics[i], execution.result.Diagnostics[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		return left.Message < right.Message
	})
}

func (execution *scanExecution) checkContext() {
	if execution.err != nil {
		return
	}
	if err := execution.ctx.Err(); err != nil {
		execution.result = Result{}
		execution.err = fmt.Errorf("scan claims in %q: %w", execution.scope, err)
	}
}

func validatePaths(repoRoot, scope string) (string, string, error) {
	validation := &pathValidation{repoRoot: repoRoot, scope: scope}
	validation.requireAbsolutePaths()
	validation.cleanAndRelativize()
	validation.inspectRepositoryRoot()
	validation.inspectScopeComponents()
	validation.inspectScope()
	return validation.repoRoot, validation.scope, validation.err
}

type pathValidation struct {
	repoRoot string
	scope    string
	relative string
	err      error
}

func (validation *pathValidation) requireAbsolutePaths() {
	switch {
	case !filepath.IsAbs(validation.repoRoot):
		validation.err = fmt.Errorf("repository root must be an absolute path: %q", validation.repoRoot)
	case !filepath.IsAbs(validation.scope):
		validation.err = fmt.Errorf("scope must be an absolute path: %q", validation.scope)
	}
}

func (validation *pathValidation) cleanAndRelativize() {
	if validation.err != nil {
		return
	}
	validation.repoRoot = filepath.Clean(validation.repoRoot)
	validation.scope = filepath.Clean(validation.scope)
	validation.relative, validation.err = filepath.Rel(validation.repoRoot, validation.scope)
	if validation.err != nil {
		validation.err = fmt.Errorf("resolve scope relative to repository root: %w", validation.err)
		return
	}
	if pathOutside(validation.relative) {
		validation.err = fmt.Errorf(
			"scope %q is outside repository root %q",
			validation.scope,
			validation.repoRoot,
		)
	}
}

func pathOutside(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (validation *pathValidation) inspectRepositoryRoot() {
	if validation.err != nil {
		return
	}
	info, err := os.Lstat(validation.repoRoot)
	if err != nil {
		validation.err = fmt.Errorf("inspect repository root %q: %w", validation.repoRoot, err)
		return
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		validation.err = fmt.Errorf("repository root must not be a symbolic link: %q", validation.repoRoot)
	case !info.IsDir():
		validation.err = fmt.Errorf("repository root is not a directory: %q", validation.repoRoot)
	}
}

func (validation *pathValidation) inspectScopeComponents() {
	if validation.err != nil || validation.relative == "." {
		return
	}
	current := validation.repoRoot
	for _, part := range strings.Split(validation.relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			validation.err = fmt.Errorf("inspect scope path %q: %w", current, err)
			break
		}
		if info.Mode()&os.ModeSymlink != 0 {
			validation.err = fmt.Errorf("scope path must not traverse a symbolic link: %q", current)
			break
		}
	}
}

func (validation *pathValidation) inspectScope() {
	if validation.err != nil {
		return
	}
	info, err := os.Lstat(validation.scope)
	if err != nil {
		validation.err = fmt.Errorf("inspect scope %q: %w", validation.scope, err)
		return
	}
	if !info.IsDir() && (!info.Mode().IsRegular() || filepath.Ext(validation.scope) != ".go") {
		validation.err = fmt.Errorf(
			"scope is not a directory or supported source file (expected a .go file): %q",
			validation.scope,
		)
	}
}

func scanFile(ctx context.Context, repoRoot, filename string) (Result, error) {
	scan := &sourceScan{ctx: ctx, repoRoot: repoRoot, filename: filename}
	scan.checkContext()
	scan.resolveRelativePath()
	scan.parseFile()
	scan.collectClaims()
	scan.assignClaimIDs()
	return scan.result, scan.err
}

type sourceScan struct {
	ctx      context.Context
	repoRoot string
	filename string
	relative string
	files    *token.FileSet
	parsed   *ast.File
	result   Result
	err      error
	skip     bool
}

func (scan *sourceScan) checkContext() {
	scan.err = scan.ctx.Err()
}

func (scan *sourceScan) resolveRelativePath() {
	if scan.err != nil {
		return
	}
	scan.relative, scan.err = filepath.Rel(scan.repoRoot, scan.filename)
	if scan.err != nil {
		scan.err = fmt.Errorf(
			"resolve source path %q relative to repository root: %w",
			scan.filename,
			scan.err,
		)
		return
	}
	scan.relative = filepath.ToSlash(scan.relative)
}

func (scan *sourceScan) parseFile() {
	if scan.err != nil {
		return
	}
	scan.files = token.NewFileSet()
	parsed, parseErr := parser.ParseFile(scan.files, scan.filename, nil, parser.ParseComments)
	scan.parsed = parsed
	if contextErr := scan.ctx.Err(); contextErr != nil {
		scan.err = contextErr
		return
	}
	if parsed != nil && ast.IsGenerated(parsed) {
		scan.skip = true
		return
	}
	if parseErr != nil {
		scan.result.Diagnostics = []Diagnostic{malformedFileDiagnostic(scan.relative, parseErr)}
		scan.skip = true
	}
}

func (scan *sourceScan) collectClaims() {
	if scan.err != nil || scan.skip {
		return
	}
	subjects := indexSubjects(scan.files, scan.parsed)
	for _, group := range scan.parsed.Comments {
		for _, claim := range parseCommentGroup(group) {
			scan.appendClaim(subjects, claim)
		}
	}
}

func (scan *sourceScan) appendClaim(subjects subjectIndex, claim parsedClaim) {
	markerLine := sourceLine(scan.files, claim.comment.Slash)
	if claim.text == "" {
		scan.result.Diagnostics = append(scan.result.Diagnostics, Diagnostic{
			File:    scan.relative,
			Line:    markerLine,
			Message: fmt.Sprintf("%s claim is empty", claim.marker),
		})
		return
	}
	subject := subjects.forComment(claim.comment)
	scan.result.Claims = append(scan.result.Claims, Claim{
		ID:         claim.id,
		Marker:     claim.marker,
		Text:       claim.text,
		Package:    scan.parsed.Name.Name,
		Kind:       subject.kind,
		Symbol:     subject.symbol,
		File:       scan.relative,
		MarkerLine: markerLine,
		StartLine:  sourceLine(scan.files, subject.start),
		EndLine:    sourceLine(scan.files, subject.end),
	})
}

// IDs depend only on source order within this file, never on the scan scope or
// claim text. Named claims also count so naming a claim does not renumber others.
func (scan *sourceScan) assignClaimIDs() {
	counts := make(map[string]int)
	for index := range scan.result.Claims {
		claim := &scan.result.Claims[index]
		parts := strings.FieldsFunc(strings.ToLower(claim.Symbol), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		base := strings.Join(parts, "-") + "-" + strings.ToLower(string(claim.Marker))
		counts[base]++
		if claim.ID == "" {
			claim.ID = fmt.Sprintf("%s-%d", base, counts[base])
		}
	}
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
	id      string
	text    string
}

func parseCommentGroup(group *ast.CommentGroup) []parsedClaim {
	claims := make([]parsedClaim, 0)
	for index, comment := range group.List {
		marker, id, firstLine, ok := markerText(comment.Text)
		if !ok {
			continue
		}
		claims = append(claims, parsedClaim{
			comment: comment,
			marker:  marker,
			id:      id,
			text:    strings.Join(claimLines(group, index, firstLine), "\n"),
		})
	}
	return claims
}

func claimLines(group *ast.CommentGroup, markerIndex int, firstLine string) []string {
	lines := make([]string, 0, 1)
	if firstLine != "" {
		lines = append(lines, firstLine)
	}
	for nextIndex := markerIndex + 1; nextIndex < len(group.List); nextIndex++ {
		next := group.List[nextIndex]
		if startsClaim(next.Text) {
			break
		}
		line, isLineComment := lineCommentText(next.Text)
		if !isLineComment || line == "" {
			break
		}
		lines = append(lines, line)
	}
	return lines
}

func startsClaim(comment string) bool {
	_, _, _, marker := markerText(comment)
	return marker || resemblesMarker(comment)
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
		suffix := remainder[len(marker):]
		return strings.HasPrefix(suffix, ":") ||
			(strings.HasPrefix(suffix, " ") || strings.HasPrefix(suffix, "\t")) && strings.Contains(suffix, ":")
	}
	return false
}

func markerText(raw string) (Marker, string, string, bool) {
	for _, marker := range []Marker{MarkerInvariant, MarkerPrecondition, MarkerPostcondition, MarkerAssertion} {
		suffix, ok := strings.CutPrefix(raw, "// "+string(marker))
		if !ok {
			continue
		}
		if text, ok := strings.CutPrefix(suffix, ":"); ok {
			return marker, "", trimHorizontal(text), true
		}
		if named, ok := strings.CutPrefix(suffix, " "); ok {
			id, text, found := strings.Cut(named, ":")
			if found && validClaimID(id) {
				return marker, id, trimHorizontal(text), true
			}
		}
	}
	return "", "", "", false
}

func validClaimID(id string) bool {
	if id == "" {
		return false
	}
	for index, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || index > 0 && strings.ContainsRune("-_.", r) {
			continue
		}
		return false
	}
	return true
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
			indexTypeSpecification(fset, index, declaration, specification)
		case *ast.ValueSpec:
			indexValueSpecification(index, declaration, specification)
		}
	}
}

func indexTypeSpecification(fset *token.FileSet, index *subjectIndex, declaration *ast.GenDecl, specification *ast.TypeSpec) {
	if declaration.Tok != token.TYPE {
		return
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
}

func indexValueSpecification(index *subjectIndex, declaration *ast.GenDecl, specification *ast.ValueSpec) {
	if declaration.Tok != token.CONST && declaration.Tok != token.VAR {
		return
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
