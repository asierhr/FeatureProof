package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"sort"
	"strconv"
	"strings"
)

// methodOrder defines the stable OpenAPI HTTP method ordering used in catalogs and context.
var methodOrder = map[string]int{"GET": 0, "PUT": 1, "POST": 2, "DELETE": 3, "OPTIONS": 4, "HEAD": 5, "PATCH": 6, "TRACE": 7}

// normalizeDocument validates the required OpenAPI shape and creates an owned deterministic snapshot.
func normalizeDocument(ctx context.Context, root map[string]any, version, file string, omitted *OmissionCounts) ([]OperationDescriptor, []SecurityScheme, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, cancelledError(file+"#", err)
	}
	info, ok := root["info"].(map[string]any)
	if !ok {
		return nil, nil, typedError(InvalidDocument, file+"#/info", errors.New("missing info object"))
	}
	if _, err := requiredString(info, "title", file+"#/info/title"); err != nil {
		return nil, nil, err
	}
	if _, err := requiredString(info, "version", file+"#/info/version"); err != nil {
		return nil, nil, err
	}
	if components, exists := root["components"]; exists {
		if _, ok := components.(map[string]any); !ok {
			return nil, nil, typedError(InvalidDocument, file+"#/components", errors.New("components must be object"))
		}
	}
	paths, ok := root["paths"].(map[string]any)
	if !ok {
		return nil, nil, typedError(InvalidDocument, file+"#/paths", errors.New("missing paths object"))
	}
	countOmissions(root, omitted)
	schemes, err := normalizeSchemes(root, file, version)
	if err != nil {
		return nil, nil, err
	}
	if err := validateComponents(root, file, version); err != nil {
		return nil, nil, err
	}
	var globalSec []SecurityRequirement
	if securityRaw, exists := root["security"]; exists {
		globalSec, _, err = normalizeSecurity(securityRaw, file+"#/security")
		if err != nil {
			return nil, nil, err
		}
	}
	var ops []OperationDescriptor
	ids := map[string]bool{}
	for pathIndex, path := range sortedKeys(paths) {
		pathLocation := file + "#/paths/item[" + strconv.Itoa(pathIndex) + "]"
		if err := ctx.Err(); err != nil {
			return nil, nil, cancelledError(file+"#/paths", err)
		}
		if !strings.HasPrefix(path, "/") {
			return nil, nil, typedError(InvalidDocument, pathLocation, errors.New("path must start with slash"))
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			return nil, nil, typedError(InvalidDocument, pathLocation, errors.New("path item must be object"))
		}
		var pathParams []Parameter
		if parametersRaw, exists := item["parameters"]; exists {
			pathParams, err = normalizeParameters(parametersRaw, pathLocation+"/parameters", version)
			if err != nil {
				return nil, nil, err
			}
		}
		methods := make([]string, 0)
		for raw := range item {
			u := strings.ToUpper(raw)
			if _, ok := methodOrder[u]; ok {
				methods = append(methods, u)
			}
		}
		sort.Slice(methods, func(i, j int) bool { return methodOrder[methods[i]] < methodOrder[methods[j]] })
		for _, method := range methods {
			raw := item[strings.ToLower(method)]
			opObj, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, typedError(InvalidDocument, pathLocation+"/"+strings.ToLower(method), errors.New("operation must be object"))
			}
			opLocation := pathLocation + "/" + strings.ToLower(method)
			op, err := normalizeOperation(opObj, pathParams, OperationKey{Method: method, Path: path}, globalSec, opLocation, version)
			if err != nil {
				return nil, nil, err
			}
			if op.OperationID != "" {
				if ids[op.OperationID] {
					return nil, nil, typedError(InvalidDocument, opLocation+"/operationId", errors.New("duplicate operationId"))
				}
				ids[op.OperationID] = true
			}
			ops = append(ops, op)
		}
	}
	if err := validateSecurityReferences(ops, schemes, file); err != nil {
		return nil, nil, err
	}
	return ops, schemes, nil
}

// validateComponents validates every reusable object type interpreted by the inspector, including unreachable entries.
func validateComponents(root map[string]any, file, version string) error {
	components, _ := root["components"].(map[string]any)
	if components == nil {
		return nil
	}
	validators := map[string]func(any, string) error{
		"schemas": func(value any, location string) error {
			_, err := normalizeSchema(value, location, version)
			return err
		},
		"parameters": func(value any, location string) error {
			_, err := normalizeParameters([]any{value}, location, version)
			return err
		},
		"responses": func(value any, location string) error {
			_, err := normalizeResponse(value, location, version)
			return err
		},
		"requestBodies": func(value any, location string) error {
			_, err := normalizeRequestBody(value, location, version)
			return err
		},
		"headers": func(value any, location string) error {
			_, err := normalizeResponse(map[string]any{"description": "component", "headers": map[string]any{"header": value}}, location, version)
			return err
		},
	}
	for _, section := range sortedKeys(validators) {
		raw, exists := components[section]
		if !exists {
			continue
		}
		entries, ok := raw.(map[string]any)
		if !ok {
			return typedError(InvalidDocument, file+"#/components/"+section, errors.New("component section must be object"))
		}
		for entryIndex, name := range sortedKeys(entries) {
			location := file + "#/components/" + section + "/item[" + strconv.Itoa(entryIndex) + "]"
			if err := validators[section](entries[name], location); err != nil {
				return err
			}
		}
	}
	return nil
}

// normalizeOperation builds one descriptor with effective parameters and security.
func normalizeOperation(obj map[string]any, pathParams []Parameter, key OperationKey, global []SecurityRequirement, loc, version string) (OperationDescriptor, error) {
	id, err := stringValue(obj, "operationId", loc)
	if err != nil {
		return OperationDescriptor{}, err
	}
	var own []Parameter
	if parametersRaw, exists := obj["parameters"]; exists {
		own, err = normalizeParameters(parametersRaw, loc+"/parameters", version)
		if err != nil {
			return OperationDescriptor{}, err
		}
	}
	params := mergeParameters(pathParams, own)
	var rb *RequestBody
	if requestBodyRaw, exists := obj["requestBody"]; exists {
		rb, err = normalizeRequestBody(requestBodyRaw, loc+"/requestBody", version)
		if err != nil {
			return OperationDescriptor{}, err
		}
	}
	responsesRaw, ok := obj["responses"].(map[string]any)
	if !ok || len(responsesRaw) == 0 {
		return OperationDescriptor{}, typedError(InvalidDocument, loc+"/responses", errors.New("responses required"))
	}
	responses := map[string]Response{}
	for selectorIndex, selector := range sortedKeys(responsesRaw) {
		responseLocation := loc + "/responses/item[" + strconv.Itoa(selectorIndex) + "]"
		if !validResponseSelector(selector) {
			return OperationDescriptor{}, typedError(InvalidDocument, responseLocation, errors.New("invalid response selector"))
		}
		r, err := normalizeResponse(responsesRaw[selector], responseLocation, version)
		if err != nil {
			return OperationDescriptor{}, err
		}
		responses[strings.ToUpper(selector)] = r
	}
	security := global
	if raw, exists := obj["security"]; exists {
		security, _, err = normalizeSecurity(raw, loc+"/security")
		if err != nil {
			return OperationDescriptor{}, err
		}
	}
	return OperationDescriptor{Key: key, OperationID: id, Parameters: params, RequestBody: rb, Responses: responses, Security: security}, nil
}

// validResponseSelector accepts exact HTTP statuses, class ranges and default.
func validResponseSelector(selector string) bool {
	upper := strings.ToUpper(selector)
	if upper == "DEFAULT" {
		return true
	}
	if len(upper) != 3 || upper[0] < '1' || upper[0] > '5' {
		return false
	}
	if upper[1:] == "XX" {
		return true
	}
	_, err := strconv.Atoi(upper)
	return err == nil
}

// validateSecurityReferences ensures every effective requirement names a declared scheme.
func validateSecurityReferences(operations []OperationDescriptor, schemes []SecurityScheme, file string) error {
	declared := make(map[string]bool, len(schemes))
	for _, scheme := range schemes {
		declared[scheme.Name] = true
	}
	for operationIndex, operation := range operations {
		for _, alternative := range operation.Security {
			for name := range alternative.Schemes {
				if !declared[name] {
					return typedError(InvalidDocument, file+"#/operations/item["+strconv.Itoa(operationIndex)+"]/security", errors.New("unknown security scheme"))
				}
			}
		}
	}
	return nil
}

// normalizeParameters validates and deduplicates one parameter list.
func normalizeParameters(raw any, location, version string) ([]Parameter, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, typedError(InvalidDocument, location, errors.New("parameters must be array"))
	}
	out := make([]Parameter, 0, len(list))
	seen := map[string]bool{}
	for i, v := range list {
		parameterLocation := location + "/item[" + strconv.Itoa(i) + "]"
		m, ok := v.(map[string]any)
		if !ok {
			return nil, typedError(InvalidDocument, parameterLocation, errors.New("parameter must be object"))
		}
		name, err := requiredString(m, "name", parameterLocation+"/name")
		if err != nil {
			return nil, err
		}
		in, err := requiredString(m, "in", parameterLocation+"/in")
		if err != nil {
			return nil, err
		}
		if in != "query" && in != "header" && in != "path" && in != "cookie" {
			return nil, typedError(InvalidDocument, parameterLocation+"/in", errors.New("unsupported parameter location"))
		}
		identity := name + "\x00" + in
		if seen[identity] {
			return nil, typedError(InvalidDocument, parameterLocation, errors.New("duplicate parameter"))
		}
		seen[identity] = true
		_, hasSchema := m["schema"]
		_, hasContent := m["content"]
		if hasSchema == hasContent {
			return nil, typedError(InvalidDocument, parameterLocation, errors.New("parameter requires exactly one schema or content"))
		}
		s, err := normalizeSchema(m["schema"], parameterLocation+"/schema", version)
		if err != nil {
			return nil, err
		}
		var content map[string]MediaType
		if hasContent {
			content, err = normalizeContent(m["content"], parameterLocation+"/content", version)
			if err != nil {
				return nil, err
			}
		}
		if hasContent && len(content) != 1 {
			return nil, typedError(InvalidDocument, parameterLocation+"/content", errors.New("parameter content requires one media type"))
		}
		required, err := optionalBool(m, "required", parameterLocation+"/required")
		if err != nil {
			return nil, err
		}
		if in == "path" && !required {
			return nil, typedError(InvalidDocument, parameterLocation+"/required", errors.New("path parameter must be required"))
		}
		out = append(out, Parameter{Name: name, In: in, Required: required, Schema: s, Content: content})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].In != out[j].In {
			return out[i].In < out[j].In
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// mergeParameters applies OpenAPI operation-level override by name and location.
func mergeParameters(path, operation []Parameter) []Parameter {
	m := map[string]Parameter{}
	for _, p := range path {
		m[p.Name+"\x00"+p.In] = p
	}
	for _, p := range operation {
		m[p.Name+"\x00"+p.In] = p
	}
	out := make([]Parameter, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].In != out[j].In {
			return out[i].In < out[j].In
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// normalizeRequestBody translates request content into owned models.
func normalizeRequestBody(raw any, location, version string) (*RequestBody, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, typedError(InvalidDocument, location, errors.New("requestBody must be object"))
	}
	contentRaw, exists := m["content"]
	if !exists {
		return nil, typedError(InvalidDocument, location+"/content", errors.New("request body content required"))
	}
	content, err := normalizeContent(contentRaw, location+"/content", version)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, typedError(InvalidDocument, location+"/content", errors.New("request body content required"))
	}
	required, err := optionalBool(m, "required", location+"/required")
	if err != nil {
		return nil, err
	}
	return &RequestBody{Required: required, Content: content}, nil
}

// normalizeResponse translates declared headers and representations.
func normalizeResponse(raw any, location, version string) (Response, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return Response{}, typedError(InvalidDocument, location, errors.New("response must be object"))
	}
	if _, err := requiredString(m, "description", location+"/description"); err != nil {
		return Response{}, err
	}
	out := Response{Headers: map[string]Header{}}
	if hraw, exists := m["headers"]; exists {
		hm, ok := hraw.(map[string]any)
		if !ok {
			return Response{}, typedError(InvalidDocument, location+"/headers", errors.New("headers must be object"))
		}
		for headerIndex, name := range sortedKeys(hm) {
			headerLocation := location + "/headers/item[" + strconv.Itoa(headerIndex) + "]"
			h, ok := hm[name].(map[string]any)
			if !ok {
				return Response{}, typedError(InvalidDocument, headerLocation, errors.New("header must be object"))
			}
			_, hasSchema := h["schema"]
			_, hasContent := h["content"]
			if hasSchema == hasContent {
				return Response{}, typedError(InvalidDocument, headerLocation, errors.New("header requires exactly one schema or content"))
			}
			schema, err := normalizeSchema(h["schema"], headerLocation+"/schema", version)
			if err != nil {
				return Response{}, err
			}
			var content map[string]MediaType
			if hasContent {
				content, err = normalizeContent(h["content"], headerLocation+"/content", version)
				if err != nil {
					return Response{}, err
				}
			}
			if hasContent && len(content) != 1 {
				return Response{}, typedError(InvalidDocument, headerLocation+"/content", errors.New("header content requires one media type"))
			}
			required, err := optionalBool(h, "required", headerLocation+"/required")
			if err != nil {
				return Response{}, err
			}
			out.Headers[name] = Header{Required: required, Schema: schema, Content: content}
		}
	}
	if contentRaw, exists := m["content"]; exists {
		content, err := normalizeContent(contentRaw, location+"/content", version)
		if err != nil {
			return Response{}, err
		}
		out.Content = content
	}
	return out, nil
}

// normalizeContent translates media-type schemas in stable map form.
func normalizeContent(raw any, location, version string) (map[string]MediaType, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, typedError(InvalidDocument, location, errors.New("content must be object"))
	}
	out := map[string]MediaType{}
	for mediaIndex, mt := range sortedKeys(m) {
		mediaLocation := location + "/item[" + strconv.Itoa(mediaIndex) + "]"
		if parsed, _, err := mime.ParseMediaType(mt); err != nil || parsed == "" {
			return nil, typedError(InvalidDocument, mediaLocation, errors.New("invalid media type"))
		}
		entry, ok := m[mt].(map[string]any)
		if !ok {
			return nil, typedError(InvalidDocument, mediaLocation, errors.New("media type must be object"))
		}
		schema, err := normalizeSchema(entry["schema"], mediaLocation+"/schema", version)
		if err != nil {
			return nil, err
		}
		out[strings.ToLower(mt)] = MediaType{Schema: schema}
	}
	return out, nil
}

// normalizeSchema converts the validation subset and preserves recursive references symbolically.
func normalizeSchema(raw any, location, version string) (*Schema, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, typedError(InvalidDocument, location, errors.New("schema must be object"))
	}
	if ref, ok := m["x-recursive-ref"].(string); ok {
		return &Schema{ID: ref}, nil
	}
	validator, err := compileSchemaValidator(m, version)
	if err != nil {
		return nil, typedError(InvalidDocument, location, err)
	}
	s := &Schema{Properties: map[string]*Schema{}}
	s.validator = validator
	if t, ok := m["type"].(string); ok {
		s.Type = t
	} else if types, ok := m["type"].([]any); ok {
		for _, v := range types {
			if v == "null" {
				s.Nullable = true
			} else if text, ok := v.(string); ok {
				s.Type = text
			}
		}
	}
	s.Format, _ = m["format"].(string)
	s.Nullable = s.Nullable || boolValue(m["nullable"])
	s.ReadOnly = boolValue(m["readOnly"])
	s.WriteOnly = boolValue(m["writeOnly"])
	if req, ok := m["required"].([]any); ok {
		for _, v := range req {
			str, ok := v.(string)
			if !ok {
				return nil, typedError(InvalidDocument, location+"/required", errors.New("required entries must be strings"))
			}
			s.Required = append(s.Required, str)
		}
		sort.Strings(s.Required)
	}
	if enum, ok := m["enum"].([]any); ok {
		s.Enum = cloneJSONSlice(enum)
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for propertyIndex, name := range sortedKeys(props) {
			child, err := normalizeSchema(props[name], location+"/properties/item["+strconv.Itoa(propertyIndex)+"]", version)
			if err != nil {
				return nil, err
			}
			s.Properties[name] = child
		}
	}
	if item, exists := m["items"]; exists {
		child, err := normalizeSchema(item, location+"/items", version)
		if err != nil {
			return nil, err
		}
		s.Items = child
	}
	if allOf, ok := m["allOf"].([]any); ok {
		s.AllOf = make([]*Schema, 0, len(allOf))
		for schemaIndex, rawSchema := range allOf {
			child, err := normalizeSchema(rawSchema, location+"/allOf/item["+strconv.Itoa(schemaIndex)+"]", version)
			if err != nil {
				return nil, err
			}
			s.AllOf = append(s.AllOf, child)
		}
	}
	if rawMinimum, exists := m["minProperties"]; exists {
		minimum, err := nonNegativeInteger(rawMinimum)
		if err != nil {
			return nil, typedError(InvalidDocument, location+"/minProperties", err)
		}
		s.MinProperties = &minimum
	}
	return s, nil
}

// nonNegativeInteger reads JSON Schema count keywords without accepting fractional or negative values.
func nonNegativeInteger(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("count must be an integer")
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || parsed < 0 || int64(int(parsed)) != parsed {
		return 0, errors.New("count must be a non-negative integer")
	}
	return int(parsed), nil
}

// normalizeSecurity preserves OR alternatives, AND scheme sets and stable scopes.
func normalizeSecurity(raw any, location string) ([]SecurityRequirement, bool, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, true, typedError(InvalidDocument, location, errors.New("security must be array"))
	}
	out := make([]SecurityRequirement, 0, len(list))
	for _, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, true, typedError(InvalidDocument, location, errors.New("security requirement must be object"))
		}
		r := SecurityRequirement{Schemes: map[string][]string{}}
		for _, name := range sortedKeys(m) {
			rawScopes, ok := m[name].([]any)
			if !ok {
				return nil, true, typedError(InvalidDocument, location, errors.New("security scopes must be array"))
			}
			scopes := make([]string, 0, len(rawScopes))
			for _, v := range rawScopes {
				s, ok := v.(string)
				if !ok {
					return nil, true, typedError(InvalidDocument, location, errors.New("scope must be string"))
				}
				scopes = append(scopes, s)
			}
			sort.Strings(scopes)
			r.Schemes[name] = scopes
		}
		out = append(out, r)
	}
	return out, true, nil
}

// normalizeSchemes extracts only contractual security metadata from components.
func normalizeSchemes(root map[string]any, file, version string) ([]SecurityScheme, error) {
	components, _ := root["components"].(map[string]any)
	if components == nil {
		return nil, nil
	}
	rawValue, exists := components["securitySchemes"]
	if !exists {
		return nil, nil
	}
	raw, ok := rawValue.(map[string]any)
	if !ok {
		return nil, typedError(InvalidDocument, file+"#/components/securitySchemes", errors.New("security schemes must be object"))
	}
	out := make([]SecurityScheme, 0, len(raw))
	for schemeIndex, name := range sortedKeys(raw) {
		location := file + "#/components/securitySchemes/item[" + strconv.Itoa(schemeIndex) + "]"
		m, ok := raw[name].(map[string]any)
		if !ok {
			return nil, typedError(InvalidDocument, location, errors.New("scheme must be object"))
		}
		s := SecurityScheme{Name: name, Flows: map[string]OAuthFlow{}}
		var err error
		s.Type, err = requiredString(m, "type", location+"/type")
		if err != nil {
			return nil, err
		}
		s.In, err = optionalString(m, "in", location+"/in")
		if err != nil {
			return nil, err
		}
		s.ParameterName, err = optionalString(m, "name", location+"/name")
		if err != nil {
			return nil, err
		}
		s.Scheme, err = optionalString(m, "scheme", location+"/scheme")
		if err != nil {
			return nil, err
		}
		s.OpenIDConnectURL, err = optionalString(m, "openIdConnectUrl", location+"/openIdConnectUrl")
		if err != nil {
			return nil, err
		}
		switch s.Type {
		case "apiKey":
			if s.ParameterName == "" || (s.In != "query" && s.In != "header" && s.In != "cookie") {
				return nil, typedError(InvalidDocument, location, errors.New("apiKey requires name and valid in"))
			}
		case "http":
			if s.Scheme == "" {
				return nil, typedError(InvalidDocument, location+"/scheme", errors.New("http scheme required"))
			}
		case "openIdConnect":
			if s.OpenIDConnectURL == "" {
				return nil, typedError(InvalidDocument, location+"/openIdConnectUrl", errors.New("OpenID URL required"))
			}
		case "oauth2":
			flowsValue, exists := m["flows"]
			flows, ok := flowsValue.(map[string]any)
			if !exists || !ok || len(flows) == 0 {
				return nil, typedError(InvalidDocument, location+"/flows", errors.New("OAuth flows required"))
			}
			for flowIndex, kind := range sortedKeys(flows) {
				flowLocation := location + "/flows/item[" + strconv.Itoa(flowIndex) + "]"
				if kind != "implicit" && kind != "password" && kind != "clientCredentials" && kind != "authorizationCode" {
					return nil, typedError(InvalidDocument, flowLocation, errors.New("unsupported OAuth flow"))
				}
				fraw, ok := flows[kind].(map[string]any)
				if !ok {
					return nil, typedError(InvalidDocument, flowLocation, errors.New("OAuth flow must be object"))
				}
				f := OAuthFlow{Scopes: map[string]string{}}
				f.AuthorizationURL, err = optionalString(fraw, "authorizationUrl", flowLocation+"/authorizationUrl")
				if err != nil {
					return nil, err
				}
				f.TokenURL, err = optionalString(fraw, "tokenUrl", flowLocation+"/tokenUrl")
				if err != nil {
					return nil, err
				}
				if (kind == "implicit" || kind == "authorizationCode") && f.AuthorizationURL == "" {
					return nil, typedError(InvalidDocument, flowLocation+"/authorizationUrl", errors.New("authorization URL required"))
				}
				if (kind == "password" || kind == "clientCredentials" || kind == "authorizationCode") && f.TokenURL == "" {
					return nil, typedError(InvalidDocument, flowLocation+"/tokenUrl", errors.New("token URL required"))
				}
				scopesValue, exists := fraw["scopes"]
				scopes, ok := scopesValue.(map[string]any)
				if !exists || !ok {
					return nil, typedError(InvalidDocument, flowLocation+"/scopes", errors.New("OAuth scopes required"))
				}
				for scopeIndex, scope := range sortedKeys(scopes) {
					description, ok := scopes[scope].(string)
					if !ok {
						return nil, typedError(InvalidDocument, flowLocation+"/scopes/item["+strconv.Itoa(scopeIndex)+"]", errors.New("scope description must be string"))
					}
					f.Scopes[scope] = description
				}
				s.Flows[kind] = f
			}
		case "mutualTLS":
			if strings.HasPrefix(version, "3.0.") {
				return nil, typedError(InvalidDocument, location+"/type", errors.New("security scheme type unsupported by version"))
			}
		default:
			return nil, typedError(InvalidDocument, location+"/type", errors.New("unsupported security scheme type"))
		}
		out = append(out, s)
	}
	return out, nil
}

// countOmissions counts untrusted fields always excluded from reduced context.
func countOmissions(v any, o *OmissionCounts) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			countOmissions(e, o)
		}
	case map[string]any:
		for k, e := range x {
			lower := strings.ToLower(k)
			switch {
			case lower == "example" || lower == "examples":
				o.Examples++
			case strings.HasPrefix(lower, "x-"):
				o.Extensions++
			case lower == "description" || lower == "title" || lower == "externaldocs":
				o.Documentation++
			default:
				countOmissions(e, o)
			}
		}
	}
}

// boolValue reads an optional boolean without creating ambient defaults elsewhere.
func boolValue(v any) bool { b, _ := v.(bool); return b }

// requiredString validates one mandatory non-empty OpenAPI string field without echoing its value.
func requiredString(object map[string]any, key, location string) (string, error) {
	value, ok := object[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", typedError(InvalidDocument, location, errors.New("required string missing or invalid"))
	}
	return value, nil
}

// optionalString validates an interpreted string field when it is present.
func optionalString(object map[string]any, key, location string) (string, error) {
	raw, exists := object[key]
	if !exists {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", typedError(InvalidDocument, location, errors.New("field must be string"))
	}
	return value, nil
}

// optionalBool validates an interpreted boolean field when it is present.
func optionalBool(object map[string]any, key, location string) (bool, error) {
	raw, exists := object[key]
	if !exists {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, typedError(InvalidDocument, location, errors.New("field must be boolean"))
	}
	return value, nil
}
