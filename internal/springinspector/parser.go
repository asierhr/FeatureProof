package springinspector

import (
	"context"
	"strconv"
	"strings"
	"unicode"
)

// token is one inert Java lexical unit with an exact source line.
type token struct {
	text string
	line int
}

// annotation is a parsed annotation name and bounded argument token sequence.
type annotation struct {
	name string
	args []token
	line int
}

// javaType is a local class, record or enum declaration used only for conservative schema closure.
type javaType struct {
	name, packageName, file, kind, sourceSet string
	imports                                  map[string]string
	line                                     int
	fields                                   []javaField
	enumValues                               []string
	customSerialization                      bool
	unsupportedShape                         bool
}

// javaField is one demonstrable local property or record component.
type javaField struct {
	name, typeName string
	line           int
	required       bool
}

// javaMethod is one controller method signature with its preceding annotations and body tokens.
type javaMethod struct {
	name, returnType, file, owner string
	line                          int
	annotations                   []annotation
	parameters                    []javaParameter
	body                          []token
}

// javaParameter is one lexical method parameter and its supported annotations.
type javaParameter struct {
	name, typeName string
	line           int
	annotations    []annotation
}

// javaClass is one top-level or nested class with its exact annotation/method projection.
type javaClass struct {
	name, packageName, file string
	line                    int
	annotations             []annotation
	methods                 []javaMethod
}

// javaUnit holds independently parsed declarations and import identities.
type javaUnit struct {
	file, packageName, sourceSet string
	imports                      map[string]string
	ambiguousImports             map[string]bool
	constants                    map[string][]string
	classes                      []javaClass
	types                        []javaType
	tokens                       int
}

// lexJava converts source bytes to passive tokens while discarding comments and retaining line locations.
func lexJava(ctx context.Context, source []byte, maximum int) ([]token, error) {
	result := []token{}
	line := 1
	for i := 0; i < len(source); {
		if len(result) >= maximum {
			return nil, typedError(CategoryLimitExceeded, "tokens", nil)
		}
		if err := ctx.Err(); err != nil {
			return nil, typedError(CategoryCancelled, "parse", err)
		}
		c := source[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if c == '/' && i+1 < len(source) && source[i+1] == '/' {
			i += 2
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(source) && source[i+1] == '*' {
			i += 2
			for i+1 < len(source) && !(source[i] == '*' && source[i+1] == '/') {
				if source[i] == '\n' {
					line++
				}
				i++
			}
			if i+1 >= len(source) {
				return nil, typedError(CategoryInvalidInput, "parse.syntax", nil)
			}
			i += 2
			continue
		}
		startLine := line
		if c == '"' || c == '\'' {
			quote := c
			start := i
			i++
			for i < len(source) {
				if source[i] == '\n' {
					line++
				}
				if source[i] == '\\' {
					i += 2
					continue
				}
				if i < len(source) && source[i] == quote {
					i++
					break
				}
				i++
			}
			if i > len(source) || source[i-1] != quote {
				return nil, typedError(CategoryInvalidInput, "parse.syntax", nil)
			}
			result = append(result, token{string(source[start:i]), startLine})
			continue
		}
		if unicode.IsLetter(rune(c)) || c == '_' || c == '$' {
			start := i
			i++
			for i < len(source) && (unicode.IsLetter(rune(source[i])) || unicode.IsDigit(rune(source[i])) || source[i] == '_' || source[i] == '$') {
				i++
			}
			result = append(result, token{string(source[start:i]), startLine})
			continue
		}
		if unicode.IsDigit(rune(c)) {
			start := i
			i++
			for i < len(source) && (unicode.IsDigit(rune(source[i])) || unicode.IsLetter(rune(source[i]))) {
				i++
			}
			result = append(result, token{string(source[start:i]), startLine})
			continue
		}
		result = append(result, token{string(c), startLine})
		i++
	}
	return result, nil
}

// parseJavaUnit builds a bounded declaration tree from bytes without filesystem, classpath or process callbacks.
func parseJavaUnit(ctx context.Context, file string, source []byte, maximum int) (javaUnit, error) {
	tokens, err := lexJava(ctx, source, maximum)
	if err != nil {
		return javaUnit{}, err
	}
	u := javaUnit{file: file, imports: map[string]string{}, ambiguousImports: map[string]bool{}, constants: map[string][]string{}, tokens: len(tokens)}
	for i := 0; i < len(tokens); {
		if tokens[i].text == "package" {
			value, next := qualifiedName(tokens, i+1)
			u.packageName = value
			i = next
			continue
		}
		if tokens[i].text == "import" {
			j := i + 1
			if j < len(tokens) && tokens[j].text == "static" {
				j++
			}
			value, next := qualifiedName(tokens, j)
			if value != "" {
				simple := value[strings.LastIndex(value, ".")+1:]
				if prior := u.imports[simple]; prior != "" && prior != value {
					u.ambiguousImports[simple] = true
				} else {
					u.imports[simple] = value
				}
			}
			i = next
			continue
		}
		if tokens[i].text == "class" || tokens[i].text == "record" || tokens[i].text == "enum" {
			start := i
			anns := annotationsBefore(tokens, start)
			if i+1 >= len(tokens) {
				return javaUnit{}, typedError(CategoryInvalidInput, "parse.syntax", nil)
			}
			name := tokens[i+1].text
			open := findToken(tokens, i+2, "{")
			if open < 0 {
				return javaUnit{}, typedError(CategoryInvalidInput, "parse.syntax", nil)
			}
			close := matching(tokens, open, "{", "}")
			if close < 0 {
				return javaUnit{}, typedError(CategoryInvalidInput, "parse.syntax", nil)
			}
			jt := javaType{name: name, packageName: u.packageName, file: file, kind: tokens[i].text, line: tokens[i].line}
			if jt.kind == "record" {
				left := findToken(tokens, i+2, "(")
				if left >= 0 && left < open {
					right := matching(tokens, left, "(", ")")
					jt.fields = parseRecordFields(tokens[left+1 : right])
				}
			}
			if jt.kind == "enum" {
				jt.enumValues = parseEnumValues(tokens[open+1 : close])
			}
			jt.fields = append(jt.fields, parseFields(tokens[open+1:close])...)
			jt.customSerialization = containsMethod(tokens[open+1:close], "serialize") || containsMethod(tokens[open+1:close], "deserialize") || containsCustomJackson(u, anns, tokens[open+1:close])
			jt.unsupportedShape = containsAnnotationPrefix(u, anns, "lombok.")
			u.types = append(u.types, jt)
			if jt.kind == "class" {
				u.types = append(u.types, parseNestedTypes(tokens[open+1:close], u, file, name)...)
			}
			for constant, values := range parseConstants(tokens[open+1 : close]) {
				u.constants[constant] = values
				u.constants[name+"."+constant] = values
			}
			class := javaClass{name: name, packageName: u.packageName, file: file, line: tokens[i].line, annotations: anns}
			class.methods = parseMethods(file, name, tokens[open+1:close])
			u.classes = append(u.classes, class)
			i = close + 1
			continue
		}
		if tokens[i].text == "static" && i+3 < len(tokens) && tokens[i+1].text == "final" {
			semi := findToken(tokens, i, ";")
			if semi > i {
				name := tokens[i+3].text
				eq := findTokenRange(tokens, i, semi, "=")
				if eq > 0 {
					if values, ok := literalValues(tokens[eq+1:semi], nil); ok {
						u.constants[name] = values
					}
				}
				i = semi + 1
				continue
			}
		}
		i++
	}
	return u, nil
}

// containsCustomJackson rejects source whose serialized shape depends on supported-external Jackson annotations.
func containsCustomJackson(unit javaUnit, annotations []annotation, body []token) bool {
	all := append([]annotation(nil), annotations...)
	nested, _ := parseAnnotations(body)
	all = append(all, nested...)
	for _, a := range all {
		name := a.name
		if !strings.Contains(name, ".") {
			name = unit.imports[name]
		}
		if strings.HasPrefix(name, "com.fasterxml.jackson.") {
			return true
		}
	}
	return false
}

// parseNestedTypes derives nested DTO declarations and propagates every conservative serialization-shape guard.
func parseNestedTypes(tokens []token, unit javaUnit, file, owner string) []javaType {
	out := []javaType{}
	depth := 0
	for i := 0; i < len(tokens); i++ {
		if tokens[i].text == "{" {
			depth++
			continue
		}
		if tokens[i].text == "}" {
			depth--
			continue
		}
		if depth != 0 || (tokens[i].text != "class" && tokens[i].text != "record" && tokens[i].text != "enum") {
			continue
		}
		if i+1 >= len(tokens) || !identifier(tokens[i+1].text) || i > 0 && tokens[i-1].text == "." {
			continue
		}
		name := owner + "." + tokens[i+1].text
		open := findToken(tokens, i+2, "{")
		if open < 0 {
			continue
		}
		close := matching(tokens, open, "{", "}")
		if close < 0 {
			continue
		}
		annotations := annotationsBefore(tokens, i)
		body := tokens[open+1 : close]
		jt := javaType{name: name, packageName: unit.packageName, file: file, kind: tokens[i].text, line: tokens[i].line}
		if jt.kind == "record" {
			left := findToken(tokens, i+2, "(")
			if left >= 0 && left < open {
				right := matching(tokens, left, "(", ")")
				if right > left {
					jt.fields = parseRecordFields(tokens[left+1 : right])
				}
			}
		}
		if jt.kind == "enum" {
			jt.enumValues = parseEnumValues(body)
		}
		jt.fields = append(jt.fields, parseFields(body)...)
		jt.customSerialization = containsMethod(body, "serialize") || containsMethod(body, "deserialize") || containsCustomJackson(unit, annotations, body)
		jt.unsupportedShape = containsAnnotationPrefix(unit, annotations, "lombok.")
		out = append(out, jt)
		if jt.kind == "class" {
			out = append(out, parseNestedTypes(body, unit, file, name)...)
		}
		i = close
	}
	return out
}

// qualifiedName reads a dotted Java identity through its terminating semicolon.
func qualifiedName(tokens []token, start int) (string, int) {
	parts := []string{}
	i := start
	for i < len(tokens) && tokens[i].text != ";" {
		if tokens[i].text != "." {
			parts = append(parts, tokens[i].text)
		}
		i++
	}
	return strings.Join(parts, "."), i + 1
}

// matching finds the balanced closing token for one already-open delimiter.
func matching(tokens []token, open int, left, right string) int {
	depth := 0
	for i := open; i < len(tokens); i++ {
		if tokens[i].text == left {
			depth++
		}
		if tokens[i].text == right {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// findToken returns the first exact token after a position.
func findToken(tokens []token, start int, want string) int {
	for i := start; i < len(tokens); i++ {
		if tokens[i].text == want {
			return i
		}
	}
	return -1
}

// findTokenRange returns an exact token inside a bounded half-open range.
func findTokenRange(tokens []token, start, end int, want string) int {
	for i := start; i < end; i++ {
		if tokens[i].text == want {
			return i
		}
	}
	return -1
}

// annotationsBefore collects only the contiguous annotation group immediately preceding a declaration.
func annotationsBefore(tokens []token, index int) []annotation {
	start := declarationStart(tokens, index)
	anns, _ := parseAnnotations(tokens[start+1 : index])
	return anns
}

// declarationStart finds the preceding declaration boundary while crossing braces only when they form an annotation array.
func declarationStart(tokens []token, index int) int {
	for start := index - 1; start >= 0; start-- {
		switch tokens[start].text {
		case ";", "{":
			return start
		case "}":
			depth, open := 1, start-1
			for ; open >= 0; open-- {
				if tokens[open].text == "}" {
					depth++
				}
				if tokens[open].text == "{" {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if open > 0 && (tokens[open-1].text == "=" || tokens[open-1].text == "(" || tokens[open-1].text == ",") {
				start = open - 1
				continue
			}
			return start
		}
	}
	return -1
}

// parseAnnotations parses balanced annotation names and arguments without evaluating them.
func parseAnnotations(tokens []token) ([]annotation, int) {
	out := []annotation{}
	for i := 0; i < len(tokens); {
		if tokens[i].text != "@" {
			i++
			continue
		}
		line := tokens[i].line
		i++
		parts := []string{}
		if i < len(tokens) && identifier(tokens[i].text) {
			parts = append(parts, tokens[i].text)
			i++
			for i+1 < len(tokens) && tokens[i].text == "." && identifier(tokens[i+1].text) {
				parts = append(parts, tokens[i+1].text)
				i += 2
			}
		}
		a := annotation{name: strings.Join(parts, "."), line: line}
		if i < len(tokens) && tokens[i].text == "(" {
			end := matching(tokens, i, "(", ")")
			if end < 0 {
				return out, -1
			}
			a.args = append([]token(nil), tokens[i+1:end]...)
			i = end + 1
		}
		out = append(out, a)
	}
	return out, len(tokens)
}

// identifier recognizes the lexical identifier subset needed by declaration parsing.
func identifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if !(unicode.IsLetter(r) || r == '_' || r == '$' || i > 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// parseMethods identifies method signatures at class brace depth zero.
func parseMethods(file, owner string, tokens []token) []javaMethod {
	out := []javaMethod{}
	depth := 0
	for i := 0; i < len(tokens); i++ {
		switch tokens[i].text {
		case "{":
			depth++
		case "}":
			depth--
		}
		if depth != 0 || tokens[i].text != "(" || i < 2 || tokens[i-2].text == "@" {
			continue
		}
		end := matching(tokens, i, "(", ")")
		if end < 0 || end+1 >= len(tokens) {
			continue
		}
		open := end + 1
		for open < len(tokens) && tokens[open].text != "{" && tokens[open].text != ";" {
			open++
		}
		if open >= len(tokens) || tokens[open].text != "{" {
			continue
		}
		close := matching(tokens, open, "{", "}")
		if close < 0 {
			continue
		}
		name := tokens[i-1].text
		if !identifier(name) || name == owner {
			continue
		}
		boundary := declarationStart(tokens, i-1)
		segment := tokens[boundary+1 : i-1]
		anns, _ := parseAnnotations(segment)
		plain := removeAnnotations(segment)
		for len(plain) > 0 && (plain[0].text == "public" || plain[0].text == "private" || plain[0].text == "protected" || plain[0].text == "static" || plain[0].text == "final" || plain[0].text == "synchronized") {
			plain = plain[1:]
		}
		returnType := joinType(plain)
		method := javaMethod{name: name, returnType: returnType, file: file, owner: owner, line: tokens[i-1].line, annotations: anns, parameters: parseParameters(tokens[i+1 : end]), body: append([]token(nil), tokens[open+1:close]...)}
		out = append(out, method)
		i = close
	}
	return out
}

// parseParameters splits a method parameter list at top-level commas and extracts annotations, type and name.
func parseParameters(tokens []token) []javaParameter {
	groups := splitTopLevel(tokens, ",")
	out := []javaParameter{}
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		anns, _ := parseAnnotations(group)
		plain := removeAnnotations(group)
		if len(plain) < 2 {
			continue
		}
		out = append(out, javaParameter{name: plain[len(plain)-1].text, typeName: joinType(plain[:len(plain)-1]), line: plain[len(plain)-1].line, annotations: anns})
	}
	return out
}

// removeAnnotations removes complete balanced annotation units from a parameter declaration.
func removeAnnotations(tokens []token) []token {
	out := []token{}
	for i := 0; i < len(tokens); {
		if tokens[i].text != "@" {
			out = append(out, tokens[i])
			i++
			continue
		}
		i++
		if i < len(tokens) && identifier(tokens[i].text) {
			i++
			for i+1 < len(tokens) && tokens[i].text == "." && identifier(tokens[i+1].text) {
				i += 2
			}
		}
		if i < len(tokens) && tokens[i].text == "(" {
			end := matching(tokens, i, "(", ")")
			if end < 0 {
				return out
			}
			i = end + 1
		}
	}
	return out
}

// splitTopLevel divides token lists without splitting nested expressions or generic types.
func splitTopLevel(tokens []token, separator string) [][]token {
	out := [][]token{}
	start, round, brace, angle := 0, 0, 0, 0
	for i, t := range tokens {
		switch t.text {
		case "(":
			round++
		case ")":
			round--
		case "{":
			brace++
		case "}":
			brace--
		case "<":
			angle++
		case ">":
			if angle > 0 {
				angle--
			}
		}
		if t.text == separator && round == 0 && brace == 0 && angle == 0 {
			out = append(out, tokens[start:i])
			start = i + 1
		}
	}
	out = append(out, tokens[start:])
	return out
}

// joinType reconstructs a bounded Java type without annotations or whitespace semantics.
func joinType(tokens []token) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(t.text)
	}
	return b.String()
}

// literalValues resolves only strings, arrays and exact local constants.
func literalValues(tokens []token, constants map[string][]string) ([]string, bool) {
	if len(tokens) == 0 {
		return nil, false
	}
	if tokens[0].text == "{" && tokens[len(tokens)-1].text == "}" {
		tokens = tokens[1 : len(tokens)-1]
	}
	parts := splitTopLevel(tokens, ",")
	out := []string{}
	for _, part := range parts {
		value, ok := constantStringExpression(part, constants)
		if !ok {
			return nil, false
		}
		out = append(out, value...)
	}
	return out, true
}

// constantStringExpression evaluates only bounded literal/reference concatenation without executing Java.
func constantStringExpression(tokens []token, constants map[string][]string) ([]string, bool) {
	terms := splitTopLevel(tokens, "+")
	if len(terms) == 1 && constants != nil {
		if values, ok := constants[joinType(terms[0])]; ok {
			return append([]string(nil), values...), true
		}
	}
	var value strings.Builder
	for _, term := range terms {
		if len(term) == 1 && strings.HasPrefix(term[0].text, "\"") {
			part, err := strconv.Unquote(term[0].text)
			if err != nil {
				return nil, false
			}
			value.WriteString(part)
			continue
		}
		if constants != nil {
			if values, ok := constants[joinType(term)]; ok && len(values) == 1 {
				value.WriteString(values[0])
				continue
			}
		}
		return nil, false
	}
	return []string{value.String()}, true
}

// parseConstants resolves only explicit static final String declarations with literal or literal-array initializers.
func parseConstants(tokens []token) map[string][]string {
	out := map[string][]string{}
	depth, start := 0, 0
	for i, t := range tokens {
		if t.text == "{" {
			depth++
		}
		if t.text == "}" {
			depth--
		}
		if t.text == ";" && depth == 0 {
			part := tokens[start:i]
			start = i + 1
			static, final := false, false
			for _, item := range part {
				static = static || item.text == "static"
				final = final || item.text == "final"
			}
			eq := findTokenRange(part, 0, len(part), "=")
			if static && final && eq >= 2 {
				if values, ok := literalValues(part[eq+1:], out); ok {
					out[part[eq-1].text] = values
				}
			}
		}
	}
	return out
}

// parseRecordFields derives record component properties without invoking generated accessors.
func parseRecordFields(tokens []token) []javaField {
	out := []javaField{}
	for _, part := range splitTopLevel(tokens, ",") {
		plain := removeAnnotations(part)
		if len(plain) >= 2 {
			out = append(out, javaField{name: plain[len(plain)-1].text, typeName: joinType(plain[:len(plain)-1]), line: plain[len(plain)-1].line, required: true})
		}
	}
	return out
}

// parseFields derives explicit class fields and ignores method-local or initialized dynamic declarations.
func parseFields(tokens []token) []javaField {
	out := []javaField{}
	depth, start := 0, 0
	for i, t := range tokens {
		if t.text == "{" {
			depth++
		}
		if t.text == "}" {
			depth--
		}
		if t.text == ";" && depth == 0 {
			part := tokens[start:i]
			start = i + 1
			if findTokenRange(part, 0, len(part), "(") >= 0 {
				continue
			}
			eq := findTokenRange(part, 0, len(part), "=")
			if eq >= 0 {
				part = part[:eq]
			}
			plain := removeAnnotations(part)
			public := len(plain) > 0 && plain[0].text == "public"
			for len(plain) > 0 && (plain[0].text == "public" || plain[0].text == "private" || plain[0].text == "protected" || plain[0].text == "static" || plain[0].text == "final") {
				plain = plain[1:]
			}
			if public && len(plain) >= 2 {
				out = append(out, javaField{name: plain[len(plain)-1].text, typeName: joinType(plain[:len(plain)-1]), line: plain[len(plain)-1].line})
			}
		}
	}
	return out
}

// containsAnnotationPrefix detects unsupported generated/custom structural annotations by exact qualified prefix.
func containsAnnotationPrefix(unit javaUnit, annotations []annotation, prefix string) bool {
	for _, candidate := range annotations {
		identity := candidate.name
		if imported := unit.imports[identity]; imported != "" {
			identity = imported
		}
		if strings.HasPrefix(identity, prefix) {
			return true
		}
	}
	return false
}

// parseEnumValues retains only the leading comma-separated identifier section of an enum.
func parseEnumValues(tokens []token) []string {
	end := len(tokens)
	if semi := findToken(tokens, 0, ";"); semi >= 0 {
		end = semi
	}
	out := []string{}
	for _, part := range splitTopLevel(tokens[:end], ",") {
		if len(part) == 1 && identifier(part[0].text) {
			out = append(out, part[0].text)
		}
	}
	return out
}

// containsMethod detects custom serialization hooks that make structural projection unsafe.
func containsMethod(tokens []token, name string) bool {
	for i := 1; i+1 < len(tokens); i++ {
		if tokens[i].text == name && tokens[i+1].text == "(" {
			return true
		}
	}
	return false
}
