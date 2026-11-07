package main

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type jsonRecord = map[string]any

var combinatorKeys = []string{"anyOf", "oneOf", "allOf", "not", "if", "then", "else", "$ref"}
var objectKeywords = []string{
	"properties", "additionalProperties", "patternProperties", "propertyNames",
	"required", "minProperties", "maxProperties", "dependentRequired",
	"dependentSchemas", "dependencies", "unevaluatedProperties",
}
var arrayKeywords = []string{
	"items", "prefixItems", "minItems", "maxItems", "uniqueItems", "contains",
	"unevaluatedItems", "additionalItems",
}
var stringKeywords = []string{"minLength", "maxLength", "pattern", "format"}
var numericKeywords = []string{"minimum", "maximum", "multipleOf", "exclusiveMinimum", "exclusiveMaximum"}

func decodePointerSegment(segment string) string {
	decoded, err := url.PathUnescape(segment)
	if err != nil || !utf8.ValidString(decoded) {
		decoded = segment
	}
	decoded = strings.ReplaceAll(decoded, "~1", "/")
	return strings.ReplaceAll(decoded, "~0", "~")
}

func jsonTypeOf(value any) string {
	if number, ok := numericValue(value); ok {
		if math.Trunc(number) == number {
			return "integer"
		}
		return "number"
	}
	switch value := value.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case nil:
		return "null"
	case []any:
		return "array"
	case jsonRecord:
		return "object"
	default:
		_ = value
		return "string"
	}
}

func numericValue(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case int32:
		return float64(value), true
	default:
		return 0, false
	}
}

func cloneJSON(value any) any {
	switch value := value.(type) {
	case []any:
		cloned := make([]any, len(value))
		for i, item := range value {
			cloned[i] = cloneJSON(item)
		}
		return cloned
	case jsonRecord:
		cloned := make(jsonRecord, len(value))
		for key, item := range value {
			cloned[key] = cloneJSON(item)
		}
		return cloned
	default:
		return value
	}
}

func stripOrReplaceRef(value any, ref string) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = stripOrReplaceRef(item, ref)
		}
		return result
	case jsonRecord:
		if value["$ref"] == ref {
			return jsonRecord{"type": "object", "description": "Circular reference"}
		}
		result := make(jsonRecord, len(value))
		for key, item := range value {
			result[key] = stripOrReplaceRef(item, ref)
		}
		return result
	default:
		return value
	}
}

func matchesType(value any, schemaType string) bool {
	actual := jsonTypeOf(value)
	return actual == schemaType || (schemaType == "number" && actual == "integer")
}

func conjoinBounds(target, rest, merged jsonRecord, key, operation string) {
	a, aOK := numericValue(target[key])
	b, bOK := numericValue(rest[key])
	if !aOK || !bOK {
		return
	}
	if operation == "max" {
		merged[key] = math.Max(a, b)
	} else {
		merged[key] = math.Min(a, b)
	}
}

func jsonEquals(a, b any) bool {
	if aNumber, ok := numericValue(a); ok {
		bNumber, bOK := numericValue(b)
		return bOK && aNumber == bNumber
	}
	switch a := a.(type) {
	case nil:
		return b == nil
	case string:
		b, ok := b.(string)
		return ok && a == b
	case bool:
		b, ok := b.(bool)
		return ok && a == b
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !jsonEquals(a[i], b[i]) {
				return false
			}
		}
		return true
	case jsonRecord:
		b, ok := b.(jsonRecord)
		if !ok || len(a) != len(b) {
			return false
		}
		for key, aValue := range a {
			bValue, ok := b[key]
			if !ok || !jsonEquals(aValue, bValue) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func mergeRefSiblings(target, rest jsonRecord) jsonRecord {
	_, targetProperties := target["properties"]
	_, targetPatterns := target["patternProperties"]
	_, restProperties := rest["properties"]
	_, restPatterns := rest["patternProperties"]
	if (target["additionalProperties"] == false && (restProperties || restPatterns)) ||
		(rest["additionalProperties"] == false && (targetProperties || targetPatterns)) {
		return jsonRecord{"allOf": []any{target, rest}}
	}

	merged := make(jsonRecord, len(target)+len(rest))
	for key, value := range target {
		merged[key] = value
	}
	for key, value := range rest {
		merged[key] = value
	}
	conjoinBounds(target, rest, merged, "minLength", "max")
	conjoinBounds(target, rest, merged, "maxLength", "min")
	conjoinBounds(target, rest, merged, "minimum", "max")
	conjoinBounds(target, rest, merged, "maximum", "min")
	conjoinBounds(target, rest, merged, "exclusiveMinimum", "max")
	conjoinBounds(target, rest, merged, "exclusiveMaximum", "min")
	conjoinBounds(target, rest, merged, "minItems", "max")
	conjoinBounds(target, rest, merged, "maxItems", "min")
	conjoinBounds(target, rest, merged, "minProperties", "max")
	conjoinBounds(target, rest, merged, "maxProperties", "min")

	targetEnum, targetOK := target["enum"].([]any)
	restEnum, restOK := rest["enum"].([]any)
	if targetOK && restOK {
		intersection := make([]any, 0, len(targetEnum))
		for _, value := range targetEnum {
			for _, restValue := range restEnum {
				if jsonEquals(value, restValue) {
					intersection = append(intersection, value)
					break
				}
			}
		}
		merged["enum"] = intersection
	}

	targetRequired, targetHasRequired := target["required"].([]any)
	restRequired, restHasRequired := rest["required"].([]any)
	if targetHasRequired || restHasRequired {
		seen := make(map[string]bool)
		required := make([]any, 0, len(targetRequired)+len(restRequired))
		for _, list := range [][]any{targetRequired, restRequired} {
			for _, item := range list {
				name, ok := item.(string)
				if ok && !seen[name] {
					seen[name] = true
					required = append(required, name)
				}
			}
		}
		merged["required"] = required
	}

	targetPropertiesMap, targetOK := target["properties"].(jsonRecord)
	restPropertiesMap, restOK := rest["properties"].(jsonRecord)
	if targetOK && restOK {
		mergedProperties := make(jsonRecord, len(targetPropertiesMap)+len(restPropertiesMap))
		for key, value := range targetPropertiesMap {
			mergedProperties[key] = value
		}
		for key, value := range restPropertiesMap {
			if current, ok := mergedProperties[key].(jsonRecord); ok {
				if restProperty, ok := value.(jsonRecord); ok {
					mergedProperties[key] = mergeRefSiblings(current, restProperty)
					continue
				}
			}
			mergedProperties[key] = value
		}
		merged["properties"] = mergedProperties
	}
	return merged
}

var pointerArrayIndex = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func derefJSONSchema(schema jsonRecord) (any, error) {
	root := cloneJSON(schema).(jsonRecord)

	resolvePointer := func(pointer string) (any, error) {
		if pointer == "#" {
			return root, nil
		}
		path := pointer
		if strings.HasPrefix(pointer, "#/") {
			path = pointer[2:]
		} else {
			path = strings.TrimPrefix(pointer, "#")
		}
		parts := strings.Split(path, "/")
		for i := range parts {
			parts[i] = decodePointerSegment(parts[i])
		}
		var current any = root
		for _, part := range parts {
			switch node := current.(type) {
			case []any:
				if !pointerArrayIndex.MatchString(part) {
					return nil, fmt.Errorf("Unable to resolve reference path: %s", pointer)
				}
				index, err := strconv.ParseUint(part, 10, 64)
				if err != nil || index >= uint64(len(node)) {
					return nil, fmt.Errorf("Unable to resolve reference path: %s", pointer)
				}
				current = node[index]
			case jsonRecord:
				value, ok := node[part]
				if !ok {
					return nil, fmt.Errorf("Unable to resolve reference path: %s", pointer)
				}
				current = value
			default:
				return nil, fmt.Errorf("Unable to resolve reference path: %s", pointer)
			}
		}
		return current, nil
	}

	var traverse func(any, map[string]bool) (any, error)
	traverse = func(node any, activeRefs map[string]bool) (any, error) {
		switch node := node.(type) {
		case []any:
			result := make([]any, len(node))
			for i, item := range node {
				traversed, err := traverse(item, activeRefs)
				if err != nil {
					return nil, err
				}
				result[i] = traversed
			}
			return result, nil
		case jsonRecord:
			ref, hasRef := node["$ref"].(string)
			if hasRef {
				if !strings.HasPrefix(ref, "#") {
					return node, nil
				}
				if activeRefs[ref] {
					targetRaw, err := resolvePointer(ref)
					if err != nil {
						return nil, err
					}
					if target, ok := targetRaw.(bool); ok {
						return target, nil
					}
					if target, ok := targetRaw.(jsonRecord); ok {
						cutoff := jsonRecord{"description": "Circular reference"}
						for key, value := range target {
							if key != "$ref" && key != "properties" && key != "items" && key != "prefixItems" {
								cutoff[key] = cloneJSON(value)
							}
						}
						if _, ok := cutoff["type"]; !ok {
							cutoff["type"] = inferTypeFromStructure(target)
						}
						if properties, ok := target["properties"].(jsonRecord); ok {
							cutoffProperties := make(jsonRecord)
							for key, value := range properties {
								if property, ok := value.(jsonRecord); ok && property["$ref"] == ref {
									continue
								}
								cutoffProperties[key] = stripOrReplaceRef(cloneJSON(value), ref)
							}
							if len(cutoffProperties) > 0 {
								cutoff["properties"] = cutoffProperties
							}
						}
						return cutoff, nil
					}
					return jsonRecord{"type": "object", "description": "Circular reference"}, nil
				}

				targetRaw, err := resolvePointer(ref)
				if err != nil {
					return nil, err
				}
				nextActive := make(map[string]bool, len(activeRefs)+1)
				for key, value := range activeRefs {
					nextActive[key] = value
				}
				nextActive[ref] = true
				target, err := traverse(targetRaw, nextActive)
				if err != nil {
					return nil, err
				}
				rest := make(jsonRecord)
				for key, value := range node {
					if key == "$ref" {
						continue
					}
					traversed, err := traverse(value, activeRefs)
					if err != nil {
						return nil, err
					}
					rest[key] = traversed
				}
				if target, ok := target.(bool); ok {
					if !target {
						return false, nil
					}
					if len(rest) > 0 {
						return rest, nil
					}
					return true, nil
				}
				targetRecord, ok := target.(jsonRecord)
				if !ok {
					return nil, fmt.Errorf("Local $ref must resolve to a JSON object or boolean")
				}
				return mergeRefSiblings(targetRecord, rest), nil
			}

			result := make(jsonRecord, len(node))
			for key, value := range node {
				traversed, err := traverse(value, activeRefs)
				if err != nil {
					return nil, err
				}
				result[key] = traversed
			}
			return result, nil
		default:
			return node, nil
		}
	}

	resolved, err := traverse(root, make(map[string]bool))
	if err != nil {
		return nil, err
	}
	if resolvedRecord, ok := resolved.(jsonRecord); ok {
		delete(resolvedRecord, "$defs")
		delete(resolvedRecord, "definitions")
	}
	return resolved, nil
}

func splitMixedEnum(values []any) []any {
	buckets := make(map[string][]any)
	order := make([]string, 0)
	for _, value := range values {
		valueType := jsonTypeOf(value)
		if _, ok := buckets[valueType]; !ok {
			order = append(order, valueType)
		}
		buckets[valueType] = append(buckets[valueType], value)
	}
	integers, hasIntegers := buckets["integer"]
	numbers, hasNumbers := buckets["number"]
	if hasIntegers && hasNumbers {
		merged := make([]any, 0, len(integers)+len(numbers))
		merged = append(merged, integers...)
		merged = append(merged, numbers...)
		delete(buckets, "integer")
		buckets["number"] = merged
		filteredOrder := order[:0]
		for _, valueType := range order {
			if valueType != "integer" {
				filteredOrder = append(filteredOrder, valueType)
			}
		}
		order = filteredOrder
	}
	branches := make([]any, 0, len(order))
	for _, valueType := range order {
		branches = append(branches, jsonRecord{"type": valueType, "enum": buckets[valueType]})
	}
	return branches
}

func inferTypeFromStructure(node jsonRecord) string {
	for _, keys := range [][]string{objectKeywords, arrayKeywords, stringKeywords, numericKeywords} {
		for _, key := range keys {
			if _, ok := node[key]; ok {
				switch key {
				case "properties", "additionalProperties", "patternProperties", "propertyNames", "required", "minProperties", "maxProperties", "dependentRequired", "dependentSchemas", "dependencies", "unevaluatedProperties":
					return "object"
				case "items", "prefixItems", "minItems", "maxItems", "uniqueItems", "contains", "unevaluatedItems", "additionalItems":
					return "array"
				case "minLength", "maxLength", "pattern", "format":
					return "string"
				default:
					return "number"
				}
			}
		}
	}
	return "string"
}

func isBranchCompatibleWithTypeSet(branch any, allowedTypes map[string]bool) bool {
	if branchType, ok := branch.(string); ok {
		return allowedTypes[branchType] || (branchType == "integer" && allowedTypes["number"])
	}
	branchRecord, ok := branch.(jsonRecord)
	if !ok {
		return true
	}
	if branchType, ok := branchRecord["type"].(string); ok {
		return allowedTypes[branchType] || (branchType == "integer" && allowedTypes["number"])
	}
	if branchTypes, ok := branchRecord["type"].([]any); ok {
		for _, value := range branchTypes {
			if branchType, ok := value.(string); ok && (allowedTypes[branchType] || (branchType == "integer" && allowedTypes["number"])) {
				return true
			}
		}
		return false
	}
	if value, ok := branchRecord["const"]; ok {
		valueType := jsonTypeOf(value)
		return allowedTypes[valueType] || (valueType == "integer" && allowedTypes["number"])
	}
	if values, ok := branchRecord["enum"].([]any); ok {
		for _, value := range values {
			valueType := jsonTypeOf(value)
			if allowedTypes[valueType] || (valueType == "integer" && allowedTypes["number"]) {
				return true
			}
		}
		return false
	}
	return true
}

func hasCombinator(node jsonRecord) bool {
	for _, key := range combinatorKeys {
		if _, ok := node[key]; ok {
			return true
		}
	}
	return false
}

func normalizeProperty(node any) {
	record, ok := node.(jsonRecord)
	if !ok {
		return
	}

	if types, ok := record["type"].([]any); ok {
		_, hasAnyOf := record["anyOf"]
		_, hasOneOf := record["oneOf"]
		if hasAnyOf || hasOneOf {
			allowedTypes := make(map[string]bool)
			for _, value := range types {
				if valueType, ok := value.(string); ok {
					allowedTypes[valueType] = true
				}
			}
			for _, key := range []string{"anyOf", "oneOf"} {
				branches, ok := record[key].([]any)
				if !ok {
					continue
				}
				filtered := make([]any, 0, len(branches))
				for _, branch := range branches {
					if !isBranchCompatibleWithTypeSet(branch, allowedTypes) {
						continue
					}
					if branchRecord, ok := branch.(jsonRecord); ok {
						if branchTypes, ok := branchRecord["type"].([]any); ok {
							narrowed := make([]any, 0, len(branchTypes))
							for _, value := range branchTypes {
								if branchType, ok := value.(string); ok && (allowedTypes[branchType] || (branchType == "integer" && allowedTypes["number"])) {
									narrowed = append(narrowed, branchType)
								}
							}
							if len(narrowed) == 1 {
								branchRecord["type"] = narrowed[0]
							} else {
								branchRecord["type"] = narrowed
							}
						}
						if values, ok := branchRecord["enum"].([]any); ok {
							filteredValues := make([]any, 0, len(values))
							for _, value := range values {
								valueType := jsonTypeOf(value)
								if allowedTypes[valueType] || (valueType == "integer" && allowedTypes["number"]) {
									filteredValues = append(filteredValues, value)
								}
							}
							branchRecord["enum"] = filteredValues
						}
						_, hasType := branchRecord["type"]
						_, hasEnum := branchRecord["enum"]
						_, hasConst := branchRecord["const"]
						if !hasType && !hasEnum && !hasConst && !hasCombinator(branchRecord) && len(allowedTypes) == 1 {
							for allowedType := range allowedTypes {
								branchRecord["type"] = allowedType
							}
						}
					}
					filtered = append(filtered, branch)
				}
				record[key] = filtered
			}
			delete(record, "type")
		} else if len(types) == 1 {
			record["type"] = types[0]
		} else if len(types) > 1 {
			branches := make([]any, len(types))
			for i, value := range types {
				if valueType, ok := value.(string); ok {
					branches[i] = jsonRecord{"type": valueType}
				} else {
					branches[i] = value
				}
			}
			record["anyOf"] = branches
			delete(record, "type")
		} else {
			delete(record, "type")
		}
	}

	_, hasAnyOf := record["anyOf"]
	_, hasOneOf := record["oneOf"]
	parentType, hasType := record["type"]
	if (hasAnyOf || hasOneOf) && hasType {
		delete(record, "type")
		if parentType, ok := parentType.(string); ok {
			for _, key := range []string{"anyOf", "oneOf"} {
				branches, ok := record[key].([]any)
				if !ok {
					continue
				}
				for _, branch := range branches {
					if branchRecord, ok := branch.(jsonRecord); ok {
						_, branchHasType := branchRecord["type"]
						_, branchHasEnum := branchRecord["enum"]
						_, branchHasConst := branchRecord["const"]
						if !branchHasType && !branchHasEnum && !branchHasConst && !hasCombinator(branchRecord) {
							branchRecord["type"] = parentType
						}
					}
				}
			}
		}
	}

	if expectedType, ok := record["type"].(string); ok {
		if values, ok := record["enum"].([]any); ok {
			filtered := make([]any, 0, len(values))
			for _, value := range values {
				if matchesType(value, expectedType) {
					filtered = append(filtered, value)
				}
			}
			record["enum"] = filtered
		}
	}

	if _, hasType := record["type"]; !hasType && !hasCombinator(record) {
		if values, ok := record["enum"].([]any); ok && len(values) > 0 {
			types := make(map[string]bool)
			for _, value := range values {
				types[jsonTypeOf(value)] = true
			}
			if len(types) == 1 {
				for valueType := range types {
					record["type"] = valueType
				}
			} else if len(types) == 2 && types["integer"] && types["number"] {
				record["type"] = "number"
			} else {
				record["anyOf"] = splitMixedEnum(values)
				delete(record, "enum")
			}
		} else if value, ok := record["const"]; ok {
			record["type"] = jsonTypeOf(value)
		} else {
			record["type"] = inferTypeFromStructure(record)
		}
	}

	removeUnknownRequiredProperties(record)
	recurseSchema(record)
}

func removeUnknownRequiredProperties(node jsonRecord) {
	properties, ok := node["properties"].(jsonRecord)
	if !ok {
		return
	}
	required, ok := node["required"].([]any)
	if !ok {
		return
	}
	known := make([]any, 0, len(required))
	for _, value := range required {
		if name, ok := value.(string); ok {
			if _, exists := properties[name]; exists {
				known = append(known, name)
			}
		}
	}
	if len(known) == 0 {
		delete(node, "required")
	} else {
		node["required"] = known
	}
}

func recurseSchema(node any) {
	record, ok := node.(jsonRecord)
	if !ok {
		return
	}
	if properties, ok := record["properties"].(jsonRecord); ok {
		for _, value := range properties {
			normalizeProperty(value)
		}
	}

	if prefixItems, ok := record["prefixItems"].([]any); ok {
		for _, value := range prefixItems {
			normalizeProperty(value)
		}
		items, hasItems := record["items"]
		hadItemsFalse := hasItems && items == false
		if !hasItems || items == true || items == false {
			switch len(prefixItems) {
			case 0:
				delete(record, "items")
			case 1:
				record["items"] = prefixItems[0]
			default:
				record["items"] = jsonRecord{"anyOf": prefixItems}
			}
		} else if items != nil {
			switch items.(type) {
			case jsonRecord, []any:
				combined := make([]any, 0, len(prefixItems)+1)
				combined = append(combined, prefixItems...)
				combined = append(combined, items)
				record["items"] = jsonRecord{"anyOf": combined}
			}
		}
		if hadItemsFalse {
			if maximum, ok := numericValue(record["maxItems"]); ok {
				record["maxItems"] = math.Min(maximum, float64(len(prefixItems)))
			} else {
				record["maxItems"] = len(prefixItems)
			}
		}
		delete(record, "prefixItems")
	}

	if items, ok := record["items"].([]any); ok {
		for _, value := range items {
			normalizeProperty(value)
		}
		if record["additionalItems"] == false {
			if maximum, ok := numericValue(record["maxItems"]); ok {
				record["maxItems"] = math.Min(maximum, float64(len(items)))
			} else {
				record["maxItems"] = len(items)
			}
		}
		delete(record, "additionalItems")
		switch len(items) {
		case 0:
			delete(record, "items")
		case 1:
			record["items"] = items[0]
		default:
			record["items"] = jsonRecord{"anyOf": items}
		}
	} else if items, ok := record["items"].(jsonRecord); ok {
		normalizeProperty(items)
	} else if items, ok := record["items"]; ok && items == false {
		record["maxItems"] = 0
		delete(record, "items")
	} else if ok && items != nil {
		switch items.(type) {
		case jsonRecord, []any:
		default:
			delete(record, "items")
		}
	}

	if additional, ok := record["additionalProperties"].(jsonRecord); ok {
		normalizeProperty(additional)
	}
	for _, key := range []string{"not", "if", "then", "else", "contains"} {
		if child, ok := record[key].(jsonRecord); ok {
			normalizeProperty(child)
		}
	}
	for _, key := range []string{"patternProperties", "dependentSchemas", "dependencies"} {
		if schemas, ok := record[key].(jsonRecord); ok {
			for _, value := range schemas {
				normalizeProperty(value)
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		branches, ok := record[key].([]any)
		if !ok {
			continue
		}
		normalized := make([]any, 0, len(branches))
		for _, value := range branches {
			switch value := value.(type) {
			case jsonRecord:
				normalizeProperty(value)
				normalized = append(normalized, value)
			case string:
				normalized = append(normalized, jsonRecord{"type": value})
			case bool:
				normalized = append(normalized, value)
			}
		}
		record[key] = normalized
	}
}

func sanitizeMcpSchema(schema any) (any, error) {
	record, ok := schema.(jsonRecord)
	if !ok {
		return schema, nil
	}
	dereferenced, err := derefJSONSchema(record)
	if err != nil {
		return nil, err
	}
	cloned := cloneJSON(dereferenced)
	normalizeProperty(cloned)
	return cloned, nil
}
