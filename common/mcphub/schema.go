package mcphub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	einoschema "github.com/cloudwego/eino/schema"
	einojsonschema "github.com/eino-contrib/jsonschema"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	maxToolSchemaBytes = 256 << 10
	maxArgumentsBytes  = 1 << 20
	maxSchemaDepth     = 32
)

func convertTool(server ServerConfig, upstream mcp.Tool) (ToolDefinition, error) {
	if err := validateToolName(upstream.Name); err != nil {
		return ToolDefinition{}, fmt.Errorf("upstream tool name is invalid: %w", err)
	}

	input, err := mcpInputSchema(upstream)
	if err != nil {
		return ToolDefinition{}, fmt.Errorf("input schema: %w", err)
	}
	input, inputMap, err := normalizeSchema(input, true)
	if err != nil {
		return ToolDefinition{}, fmt.Errorf("input schema: %w", err)
	}

	var output json.RawMessage
	if len(upstream.RawOutputSchema) > 0 {
		output, _, err = normalizeSchema(upstream.RawOutputSchema, false)
	} else if upstream.OutputSchema.Type != "" {
		encoded, marshalErr := json.Marshal(upstream.OutputSchema)
		if marshalErr != nil {
			return ToolDefinition{}, fmt.Errorf("output schema: %w", marshalErr)
		}
		output, _, err = normalizeSchema(encoded, false)
	}
	if err != nil {
		return ToolDefinition{}, fmt.Errorf("output schema: %w", err)
	}

	annotations := upstream.Annotations
	// The local read_only_tools allowlist is the authoritative retry boundary.
	// MCP annotation defaults are conservative (readOnly=false,
	// destructive=true), so treating those defaults as an override would make
	// explicitly reviewed legacy tools impossible to classify as read-only.
	readOnly := server.toolReadOnly(upstream.Name)
	destructive := !readOnly && annotations.DestructiveHint != nil && *annotations.DestructiveHint
	idempotent := annotations.IdempotentHint != nil && *annotations.IdempotentHint
	openWorld := annotations.OpenWorldHint != nil && *annotations.OpenWorldHint

	risk := RiskMedium
	if readOnly {
		risk = RiskLow
	}
	if configured, ok := server.RiskLevels[upstream.Name]; ok {
		risk = configured
	}
	if destructive {
		risk = maxRisk(risk, RiskHigh)
	}

	definition := ToolDefinition{
		Name:             server.ID + "." + upstream.Name,
		ServerID:         server.ID,
		UpstreamName:     upstream.Name,
		Description:      strings.TrimSpace(upstream.Description),
		InputSchema:      input,
		OutputSchema:     output,
		ReadOnly:         readOnly,
		Idempotent:       idempotent,
		Destructive:      destructive,
		OpenWorld:        openWorld,
		Risk:             risk,
		RequiresApproval: risk.RequiresApproval() || server.toolApprovalRequired(upstream.Name),
	}

	// Retain the decoded schema only long enough to ensure the canonical form is
	// usable for invocation validation.
	if err := validateSchemaNode(inputMap, inputMap, 0); err != nil {
		return ToolDefinition{}, fmt.Errorf("input schema: %w", err)
	}
	return definition, nil
}

func mcpInputSchema(tool mcp.Tool) (json.RawMessage, error) {
	if len(tool.RawInputSchema) > 0 {
		if tool.InputSchema.Type != "" {
			return nil, fmt.Errorf("both raw and structured schemas are set")
		}
		return append(json.RawMessage(nil), tool.RawInputSchema...), nil
	}
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func normalizeSchema(raw []byte, requireObject bool) (json.RawMessage, map[string]any, error) {
	if len(raw) == 0 || len(raw) > maxToolSchemaBytes {
		return nil, nil, fmt.Errorf("schema is empty or exceeds %d bytes", maxToolSchemaBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, nil, fmt.Errorf("schema must be a JSON object")
	}
	if err := decodeSingleJSON(raw, &decoded); err != nil {
		return nil, nil, err
	}
	if _, hasType := decoded["type"]; !hasType {
		if _, hasProperties := decoded["properties"]; hasProperties {
			decoded["type"] = "object"
		}
	}
	if requireObject && !schemaAllowsType(decoded, "object") {
		return nil, nil, fmt.Errorf("root type must include object")
	}
	if err := validateSchemaNode(decoded, decoded, 0); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, nil, fmt.Errorf("schema cannot be encoded")
	}
	return canonical, decoded, nil
}

func decodeSingleJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("schema contains invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("schema contains trailing data")
	} else if err != io.EOF {
		return fmt.Errorf("schema contains invalid trailing data")
	}
	return nil
}

func validateSchemaNode(node, root map[string]any, depth int) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("schema nesting exceeds %d levels", maxSchemaDepth)
	}
	if reference, ok := node["$ref"].(string); ok {
		if _, err := resolveLocalRef(root, reference); err != nil {
			return err
		}
	}
	if rawType, exists := node["type"]; exists {
		if err := validateSchemaType(rawType); err != nil {
			return err
		}
	}
	if pattern, ok := node["pattern"].(string); ok {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("schema contains an invalid pattern")
		}
	}

	properties, hasProperties := asObject(node["properties"])
	if node["properties"] != nil && !hasProperties {
		return fmt.Errorf("properties must be an object")
	}
	required, err := stringList(node["required"])
	if err != nil {
		return err
	}
	for _, name := range required {
		if _, exists := properties[name]; !exists {
			return fmt.Errorf("required property %q is not defined", name)
		}
	}
	for name, rawChild := range properties {
		child, ok := asObject(rawChild)
		if !ok {
			return fmt.Errorf("property %q schema must be an object", name)
		}
		if err := validateSchemaNode(child, root, depth+1); err != nil {
			return fmt.Errorf("property %q: %w", name, err)
		}
	}

	if rawItems, exists := node["items"]; exists {
		items, ok := asObject(rawItems)
		if !ok {
			return fmt.Errorf("items must be an object schema")
		}
		if err := validateSchemaNode(items, root, depth+1); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	if additional, exists := node["additionalProperties"]; exists {
		switch typed := additional.(type) {
		case bool:
		case map[string]any:
			if err := validateSchemaNode(typed, root, depth+1); err != nil {
				return fmt.Errorf("additionalProperties: %w", err)
			}
		default:
			return fmt.Errorf("additionalProperties must be a boolean or object schema")
		}
	}

	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if rawBranches, exists := node[keyword]; exists {
			branches, ok := rawBranches.([]any)
			if !ok || len(branches) == 0 {
				return fmt.Errorf("%s must be a non-empty array", keyword)
			}
			for _, rawBranch := range branches {
				branch, ok := asObject(rawBranch)
				if !ok {
					return fmt.Errorf("%s entries must be object schemas", keyword)
				}
				if err := validateSchemaNode(branch, root, depth+1); err != nil {
					return fmt.Errorf("%s: %w", keyword, err)
				}
			}
		}
	}
	return nil
}

func validateSchemaType(raw any) error {
	valid := func(value string) bool {
		switch value {
		case "object", "array", "string", "number", "integer", "boolean", "null":
			return true
		default:
			return false
		}
	}
	switch typed := raw.(type) {
	case string:
		if !valid(typed) {
			return fmt.Errorf("schema contains an unsupported type")
		}
	case []any:
		if len(typed) == 0 {
			return fmt.Errorf("schema type array cannot be empty")
		}
		for _, entry := range typed {
			name, ok := entry.(string)
			if !ok || !valid(name) {
				return fmt.Errorf("schema contains an unsupported type")
			}
		}
	default:
		return fmt.Errorf("schema type must be a string or string array")
	}
	return nil
}

func schemaAllowsType(schema map[string]any, wanted string) bool {
	raw, ok := schema["type"]
	if !ok {
		return false
	}
	switch typed := raw.(type) {
	case string:
		return typed == wanted
	case []any:
		for _, entry := range typed {
			if entry == wanted {
				return true
			}
		}
	}
	return false
}

// ValidateArguments applies the useful validation subset of JSON Schema used
// by MCP tools before any remote side effect can occur.
func (tool ToolDefinition) ValidateArguments(arguments map[string]any) error {
	if arguments == nil {
		arguments = map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > maxArgumentsBytes {
		return newError(ErrorInvalidArguments, "tool arguments are invalid or too large", err)
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(tool.InputSchema))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return newError(ErrorInvalidArguments, "tool schema is unavailable", err)
	}
	if err := validateJSONValue(arguments, root, root, "$", 0); err != nil {
		return newError(ErrorInvalidArguments, "tool arguments do not match the input schema", err)
	}
	return nil
}

func validateJSONValue(value any, schema, root map[string]any, path string, depth int) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("%s exceeds maximum nesting", path)
	}
	if reference, ok := schema["$ref"].(string); ok {
		resolved, err := resolveLocalRef(root, reference)
		if err != nil {
			return fmt.Errorf("%s has an invalid schema reference", path)
		}
		return validateJSONValue(value, resolved, root, path, depth+1)
	}

	if branches, ok := schema["allOf"].([]any); ok {
		for _, raw := range branches {
			branch, _ := asObject(raw)
			if err := validateJSONValue(value, branch, root, path, depth+1); err != nil {
				return err
			}
		}
	}
	if branches, ok := schema["anyOf"].([]any); ok {
		matched := false
		for _, raw := range branches {
			branch, _ := asObject(raw)
			if validateJSONValue(value, branch, root, path, depth+1) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s does not match any allowed schema", path)
		}
	}
	if branches, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, raw := range branches {
			branch, _ := asObject(raw)
			if validateJSONValue(value, branch, root, path, depth+1) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s must match exactly one allowed schema", path)
		}
	}

	if enum, ok := schema["enum"].([]any); ok && !matchesAnyJSON(value, enum) {
		return fmt.Errorf("%s is not an allowed enum value", path)
	}
	if constant, exists := schema["const"]; exists && !sameJSON(value, constant) {
		return fmt.Errorf("%s does not match the required constant", path)
	}

	types := schemaTypes(schema)
	if len(types) > 0 && !matchesTypes(value, types) {
		return fmt.Errorf("%s has the wrong type", path)
	}

	switch typed := value.(type) {
	case map[string]any:
		return validateObject(typed, schema, root, path, depth)
	case []any:
		return validateArray(typed, schema, root, path, depth)
	case string:
		return validateString(typed, schema, path)
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return validateNumber(value, schema, path)
	default:
		return nil
	}
}

func validateObject(value map[string]any, schema, root map[string]any, path string, depth int) error {
	properties, _ := asObject(schema["properties"])
	required, _ := stringList(schema["required"])
	for _, name := range required {
		if _, exists := value[name]; !exists {
			return fmt.Errorf("%s.%s is required", path, name)
		}
	}

	for name, childValue := range value {
		rawChild, known := properties[name]
		if known {
			childSchema, _ := asObject(rawChild)
			if err := validateJSONValue(childValue, childSchema, root, path+"."+name, depth+1); err != nil {
				return err
			}
			continue
		}
		additional, hasAdditional := schema["additionalProperties"]
		if !hasAdditional {
			continue
		}
		switch typed := additional.(type) {
		case bool:
			if !typed {
				return fmt.Errorf("%s.%s is not allowed", path, name)
			}
		case map[string]any:
			if err := validateJSONValue(childValue, typed, root, path+"."+name, depth+1); err != nil {
				return err
			}
		}
	}
	if minimum, ok := integerKeyword(schema["minProperties"]); ok && len(value) < minimum {
		return fmt.Errorf("%s has too few properties", path)
	}
	if maximum, ok := integerKeyword(schema["maxProperties"]); ok && len(value) > maximum {
		return fmt.Errorf("%s has too many properties", path)
	}
	return nil
}

func validateArray(value []any, schema, root map[string]any, path string, depth int) error {
	if minimum, ok := integerKeyword(schema["minItems"]); ok && len(value) < minimum {
		return fmt.Errorf("%s has too few items", path)
	}
	if maximum, ok := integerKeyword(schema["maxItems"]); ok && len(value) > maximum {
		return fmt.Errorf("%s has too many items", path)
	}
	items, ok := asObject(schema["items"])
	if !ok {
		return nil
	}
	for index, entry := range value {
		if err := validateJSONValue(entry, items, root, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateString(value string, schema map[string]any, path string) error {
	length := utf8.RuneCountInString(value)
	if minimum, ok := integerKeyword(schema["minLength"]); ok && length < minimum {
		return fmt.Errorf("%s is too short", path)
	}
	if maximum, ok := integerKeyword(schema["maxLength"]); ok && length > maximum {
		return fmt.Errorf("%s is too long", path)
	}
	if pattern, ok := schema["pattern"].(string); ok {
		compiled, err := regexp.Compile(pattern)
		if err != nil || !compiled.MatchString(value) {
			return fmt.Errorf("%s has an invalid format", path)
		}
	}
	return nil
}

func validateNumber(value any, schema map[string]any, path string) error {
	number, ok := numberValue(value)
	if !ok {
		return fmt.Errorf("%s is not numeric", path)
	}
	if schemaAllowsType(schema, "integer") && !schemaAllowsType(schema, "number") && math.Trunc(number) != number {
		return fmt.Errorf("%s must be an integer", path)
	}
	if minimum, ok := numberValue(schema["minimum"]); ok && number < minimum {
		return fmt.Errorf("%s is below the minimum", path)
	}
	if maximum, ok := numberValue(schema["maximum"]); ok && number > maximum {
		return fmt.Errorf("%s exceeds the maximum", path)
	}
	if minimum, ok := numberValue(schema["exclusiveMinimum"]); ok && number <= minimum {
		return fmt.Errorf("%s must exceed the exclusive minimum", path)
	}
	if maximum, ok := numberValue(schema["exclusiveMaximum"]); ok && number >= maximum {
		return fmt.Errorf("%s must be below the exclusive maximum", path)
	}
	return nil
}

func schemaTypes(schema map[string]any) []string {
	switch typed := schema["type"].(type) {
	case string:
		return []string{typed}
	case []any:
		values := make([]string, 0, len(typed))
		for _, raw := range typed {
			if value, ok := raw.(string); ok {
				values = append(values, value)
			}
		}
		return values
	default:
		return nil
	}
}

func matchesTypes(value any, types []string) bool {
	for _, expected := range types {
		switch expected {
		case "null":
			if value == nil {
				return true
			}
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "number":
			if _, ok := numberValue(value); ok {
				return true
			}
		case "integer":
			if number, ok := numberValue(value); ok && math.Trunc(number) == number {
				return true
			}
		}
	}
	return false
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func integerKeyword(value any) (int, bool) {
	number, ok := numberValue(value)
	if !ok || number < 0 || math.Trunc(number) != number || number > math.MaxInt {
		return 0, false
	}
	return int(number), true
}

func asObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func stringList(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("required must be a string array")
	}
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, entry := range raw {
		name, ok := entry.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("required must be a string array")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("required contains duplicate entries")
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result, nil
}

func resolveLocalRef(root map[string]any, reference string) (map[string]any, error) {
	if !strings.HasPrefix(reference, "#/") {
		return nil, fmt.Errorf("only local schema references are supported")
	}
	var current any = root
	for _, token := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema reference does not resolve to an object")
		}
		current, ok = object[token]
		if !ok {
			return nil, fmt.Errorf("schema reference cannot be resolved")
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema reference does not resolve to an object")
	}
	return resolved, nil
}

func matchesAnyJSON(value any, candidates []any) bool {
	for _, candidate := range candidates {
		if sameJSON(value, candidate) {
			return true
		}
	}
	return false
}

func sameJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// EinoToolInfo is the integration boundary used by the model gateway. It
// preserves the namespaced name and the complete MCP JSON Schema.
func (tool ToolDefinition) EinoToolInfo() (*einoschema.ToolInfo, error) {
	var converted einojsonschema.Schema
	if err := json.Unmarshal(tool.InputSchema, &converted); err != nil {
		return nil, newError(ErrorProtocol, "tool schema cannot be converted for Eino", err)
	}
	return &einoschema.ToolInfo{
		Name: tool.Name,
		Desc: tool.Description,
		Extra: map[string]any{
			"server_id":         tool.ServerID,
			"upstream_name":     tool.UpstreamName,
			"risk":              tool.Risk,
			"requires_approval": tool.RequiresApproval,
		},
		ParamsOneOf: einoschema.NewParamsOneOfByJSONSchema(&converted),
	}, nil
}
