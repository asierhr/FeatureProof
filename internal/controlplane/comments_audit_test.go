package controlplane

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// persistentControlPlaneBase is the reviewed feature baseline used to derive
// the complete production, command, contract and test diff without a manifest.
const persistentControlPlaneBase = "5812d7a"

// TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols audits every
// changed or new Go file, including commands, tests and contract helpers.
func TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols(t *testing.T) {
	root := persistentControlPlaneRepositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Skip("source checkout unavailable in cross-built test runtime")
	}
	paths := persistentControlPlaneGoDiff(t, root)
	for _, path := range paths {
		auditSystemicComments(t, root, path)
	}
	auditFeatureCommentFamilies(t, root, paths)
}

// persistentControlPlaneRepositoryRoot resolves the checkout from this test
// file so package working-directory differences cannot narrow the audit.
func persistentControlPlaneRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("control-plane audit source unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

// persistentControlPlaneGoDiff asks Git for tracked and untracked Go files and
// returns one deterministic, duplicate-free feature inventory.
func persistentControlPlaneGoDiff(t *testing.T, root string) []string {
	t.Helper()
	paths := map[string]bool{}
	commands := [][]string{{"diff", "--name-only", persistentControlPlaneBase, "--", "*.go"}, {"ls-files", "--others", "--exclude-standard", "--", "*.go"}}
	for _, arguments := range commands {
		output, err := exec.Command("git", append([]string{"-C", root}, arguments...)...).Output()
		if err != nil {
			t.Fatalf("feature-wide comment inventory failed: %v", err)
		}
		scanner := bufio.NewScanner(strings.NewReader(string(output)))
		for scanner.Scan() {
			path := filepath.ToSlash(strings.TrimSpace(scanner.Text()))
			if path != "" {
				paths[path] = true
			}
		}
		if err = scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	if len(result) == 0 {
		t.Fatal("feature-wide comment inventory is empty")
	}
	return result
}

// auditSystemicComments checks feature-owned types, package symbols, functions,
// methods and test helpers without demanding noise on local implementation details.
func auditSystemicComments(t *testing.T, root, relativePath string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	auditCommentRepetition(t, path, file)
	addedLines := persistentControlPlaneAddedLines(t, root, relativePath)
	for _, declaration := range file.Decls {
		if !addedLines[fileSet.Position(declaration.Pos()).Line] {
			continue
		}
		switch value := declaration.(type) {
		case *ast.FuncDecl:
			if !usefulSystemicComment(value.Doc, value.Name.Name) {
				t.Errorf("%s:%d function/helper %s lacks useful systemic comment", path, fileSet.Position(value.Pos()).Line, value.Name.Name)
			}
		case *ast.GenDecl:
			for _, raw := range value.Specs {
				if !addedLines[fileSet.Position(raw.Pos()).Line] {
					continue
				}
				var names []string
				var comment *ast.CommentGroup
				switch spec := raw.(type) {
				case *ast.TypeSpec:
					names, comment = []string{spec.Name.Name}, spec.Doc
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						names = append(names, name.Name)
					}
					comment = spec.Doc
				}
				if comment == nil {
					comment = value.Doc
				}
				for _, name := range names {
					if name == "_" {
						continue
					}
					if !usefulSystemicComment(comment, name) {
						t.Errorf("%s:%d entity/constant/variable %s lacks useful systemic comment", path, fileSet.Position(raw.Pos()).Line, name)
					}
				}
			}
		}
	}
}

// persistentControlPlaneAddedLines returns the exact new-side source lines in
// the feature diff so unchanged local implementation details are not relabeled.
func persistentControlPlaneAddedLines(t *testing.T, root, path string) map[int]bool {
	t.Helper()
	result := map[int]bool{}
	tracked := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", path).Run() == nil
	if !tracked {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		for line := 1; line <= len(strings.Split(string(raw), "\n")); line++ {
			result[line] = true
		}
		return result
	}
	output, err := exec.Command("git", "-C", root, "diff", "--unified=0", persistentControlPlaneBase, "--", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		plus := strings.Index(line, " +")
		if plus < 0 {
			t.Fatalf("invalid diff hunk for %s: %s", path, line)
		}
		end := strings.Index(line[plus+2:], " ")
		if end < 0 {
			t.Fatalf("invalid diff hunk for %s: %s", path, line)
		}
		rangeText := line[plus+2 : plus+2+end]
		parts := strings.Split(rangeText, ",")
		start, startErr := strconv.Atoi(parts[0])
		count := 1
		var countErr error
		if len(parts) == 2 {
			count, countErr = strconv.Atoi(parts[1])
		}
		if startErr != nil || countErr != nil {
			t.Fatalf("invalid diff range for %s: %s", path, rangeText)
		}
		for offset := 0; offset < count; offset++ {
			result[start+offset] = true
		}
	}
	return result
}

// usefulSystemicComment rejects empty and name-only comments while allowing a
// declaration comment to explain the role of grouped constants or variables.
func usefulSystemicComment(comment *ast.CommentGroup, name string) bool {
	if comment == nil {
		return false
	}
	text := strings.TrimSpace(comment.Text())
	lower := strings.ToLower(text)
	for _, forbidden := range []string{
		"carry the validated " + "operation state",
		"persisted authority and state consumed",
		"for the next " + "invariant enforced by",
		"this operation boundary",
		"simply stores",
		"just holds",
		"capture failures from",
		"retain the checked intermediate",
		"carry the validated domain configuration",
		"record the closed decision returned",
		"persistence or execution boundary created by",
		"represent the durable projection obtained",
		"identify the durable aggregate selected",
		"expose the explicit collaboration boundary",
		"collect the deterministic domain projection",
		"accumulate the verified outcome",
		"hold bytes from",
		"lease and event ordering to the injected clock",
		"bound cancellation and completion time",
		"retain the consumer-owned authority",
		"carry the consumer-owned work description",
		"identify the bounded filesystem location",
		"track the bounded ordering value",
		"retain the integrity or authority proof",
		"retain the comparison snapshot",
		"keeps expression",
		"keeps declaration",
		"carries the explicit success signal",
		"exposes the expression",
		"exposes the declaration",
		"exposes the range",
		"names the aggregate",
		"enumerates `",
		"confines expression",
	} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return len(strings.Fields(text)) >= 4 && !strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(text, name)), "")
}

// auditCommentRepetition rejects mass-copied prose inside one file; systemic
// comments may share terminology but cannot substitute one template for many
// unrelated responsibilities.
func auditCommentRepetition(t *testing.T, path string, file *ast.File) {
	t.Helper()
	counts := map[string]int{}
	for _, group := range file.Comments {
		text := strings.ToLower(strings.Join(strings.Fields(group.Text()), " "))
		if text == "" || strings.HasPrefix(text, "go:build") || strings.HasPrefix(text, "+build") {
			continue
		}
		counts[text]++
	}
	for text, count := range counts {
		if count > 10 {
			t.Errorf("%s repeats one generic comment %d times: %q", path, count, text)
		}
	}
}

var (
	// commentQuotedValuePattern removes literal-specific noise before comparing documentation structure.
	commentQuotedValuePattern = regexp.MustCompile("`[^`]*`|\"[^\"]*\"")
	// commentQualifiedNamePattern collapses call and selector spellings that otherwise disguise one prose template.
	commentQualifiedNamePattern = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+\b`)
	// commentCamelNamePattern collapses interpolated Go identifiers while retaining ordinary responsibility vocabulary.
	commentCamelNamePattern = regexp.MustCompile(`\b(?:[a-z]+[A-Z][A-Za-z0-9_]*|[A-Z][A-Za-z0-9_]*)\b`)
	// commentNumberPattern removes line, revision and fixture numbers from family comparison.
	commentNumberPattern = regexp.MustCompile(`\b\d+\b`)
)

// auditFeatureCommentFamilies rejects structurally repeated prose across the
// whole diff, including templates whose identifiers vary between files.
func auditFeatureCommentFamilies(t *testing.T, root string, paths []string) {
	t.Helper()
	families := map[string]int{}
	examples := map[string]string{}
	for _, relative := range paths {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, filepath.Join(root, filepath.FromSlash(relative)), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range file.Comments {
			text := strings.TrimSpace(group.Text())
			if text == "" || strings.HasPrefix(text, "go:build") || strings.HasPrefix(text, "+build") {
				continue
			}
			skeleton := systemicCommentSkeleton(text)
			families[skeleton]++
			if examples[skeleton] == "" {
				examples[skeleton] = text
			}
		}
	}
	for skeleton, count := range families {
		if count > 40 {
			t.Errorf("feature diff repeats one structural comment family %d times: %q (example %q)", count, skeleton, examples[skeleton])
		}
	}
}

// systemicCommentSkeleton removes interpolated code tokens while preserving
// the prose that reveals a mass-generated comment family.
func systemicCommentSkeleton(text string) string {
	value := commentQuotedValuePattern.ReplaceAllString(text, " <value> ")
	value = commentQualifiedNamePattern.ReplaceAllString(value, " <identifier> ")
	value = commentCamelNamePattern.ReplaceAllString(value, " <identifier> ")
	value = strings.ToLower(value)
	value = stripCommentDeclarationNames(value)
	value = commentNumberPattern.ReplaceAllString(value, " <number> ")
	return strings.Join(strings.Fields(value), " ")
}

// stripCommentDeclarationNames removes only the leading identifier list before
// a responsibility verb, leaving the explanatory sentence intact.
func stripCommentDeclarationNames(text string) string {
	verbs := map[string]bool{"bind": true, "binds": true, "let": true, "carry": true, "carries": true, "keep": true, "keeps": true, "are": true, "preserve": true, "preserves": true, "give": true, "gives": true, "transfer": true, "transfers": true, "is": true, "reports": true, "make": true, "makes": true, "hold": true, "holds": true, "capture": true, "captures": true, "resolve": true, "resolves": true, "identify": true, "identifies": true, "walk": true, "walks": true, "enumerate": true, "enumerates": true, "traverse": true, "traverses": true, "expose": true, "exposes": true, "define": true, "defines": true, "retain": true, "retains": true}
	words := strings.Fields(text)
	for index, word := range words {
		if verbs[strings.Trim(word, ",")] {
			return strings.Join(words[index:], " ")
		}
	}
	return text
}

// TestUsefulSystemicCommentRejectsBoilerplate protects R16 against known
// templates while retaining comments that state a concrete authority invariant.
func TestUsefulSystemicCommentRejectsBoilerplate(t *testing.T) {
	boilerplate := &ast.CommentGroup{List: []*ast.Comment{{Text: "// value carry the validated " + "operation state into this boundary."}}}
	if usefulSystemicComment(boilerplate, "value") {
		t.Fatal("known boilerplate satisfied systemic-comment audit")
	}
	useful := &ast.CommentGroup{List: []*ast.Comment{{Text: "// token binds the immutable backend epoch to the pending fenced mutation."}}}
	if !usefulSystemicComment(useful, "token") {
		t.Fatal("concrete authority invariant was rejected")
	}
}

// TestSystemicCommentSkeletonCollapsesInterpolatedTemplates proves changing
// identifiers cannot evade the feature-wide structural repetition check.
func TestSystemicCommentSkeletonCollapsesInterpolatedTemplates(t *testing.T) {
	first := systemicCommentSkeleton("ticker carries the time.NewTicker value into handleWithHeartbeat before its authority check")
	second := systemicCommentSkeleton("receipt carries the store.LoadReceipt value into CutoverImport before its authority check")
	if first != second {
		t.Fatalf("interpolated boilerplate escaped normalization: %q != %q", first, second)
	}
}
