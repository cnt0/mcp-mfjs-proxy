package main

import (
	"bytes"
	"encoding/json"
	"log"
	"reflect"
	"regexp"
	"sync"
)

type messageRewriter struct {
	mu        sync.Mutex
	pending   map[string]string
	toolNames map[string]string
	noRename  bool
	report    bool
	logger    *log.Logger
}

func newMessageRewriter(noRename, report bool, logger *log.Logger) *messageRewriter {
	return &messageRewriter{
		pending:   make(map[string]string),
		toolNames: make(map[string]string),
		noRename:  noRename,
		report:    report,
		logger:    logger,
	}
}

func (r *messageRewriter) rewriteClientMessage(line []byte) []byte {
	message, ending := parseMessageLine(line)
	if message == nil {
		return line
	}
	method := rawString(message["method"])
	id, hasID := messageID(message["id"])

	r.mu.Lock()
	defer r.mu.Unlock()
	if method != "" && hasID {
		r.pending[id] = method
	}
	if method != "tools/call" {
		return line
	}
	params, ok := rawObject(message["params"])
	if !ok {
		return line
	}
	name := rawString(params["name"])
	original, ok := r.toolNames[name]
	if !ok || original == name {
		return line
	}
	encodedName, _ := json.Marshal(original)
	params["name"] = encodedName
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return line
	}
	message["params"] = encodedParams
	return marshalMessageLine(message, ending, line)
}

func (r *messageRewriter) rewriteUpstreamMessage(line []byte) []byte {
	message, ending := parseMessageLine(line)
	if message == nil {
		return line
	}
	method := rawString(message["method"])
	id, hasID := messageID(message["id"])

	r.mu.Lock()
	defer r.mu.Unlock()
	if method == "notifications/tools/list_changed" {
		r.toolNames = make(map[string]string)
		if r.report {
			r.logger.Print("invalidated tool-name mapping")
		}
		return line
	}
	if !hasID {
		return line
	}
	requestMethod, ok := r.pending[id]
	if !ok {
		return line
	}
	delete(r.pending, id)
	if requestMethod != "tools/list" {
		return line
	}
	result, err := r.rewriteToolsList(message["result"])
	if err != nil {
		r.logger.Printf("sanitize tools/list response: %v", err)
		return line
	}
	if result == nil {
		return line
	}
	message["result"] = result
	return marshalMessageLine(message, ending, line)
}

func (r *messageRewriter) rewriteToolsList(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, nil
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		return nil, nil
	}

	changed := false
	schemasChanged := 0
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if !r.noRename {
			if name, ok := tool["name"].(string); ok {
				rewritten := sanitizeToolName(name)
				r.toolNames[rewritten] = name
				if rewritten != name {
					tool["name"] = rewritten
					changed = true
					if r.report {
						r.logger.Printf("renamed tool %q to %q", name, rewritten)
					}
				}
			}
		}
		for _, key := range []string{"inputSchema", "outputSchema"} {
			schema, ok := tool[key]
			if !ok {
				continue
			}
			sanitized, err := sanitizeMcpSchema(schema)
			if err != nil {
				name, _ := tool["name"].(string)
				r.logger.Printf("sanitize %s for tool %q: %v", key, name, err)
				continue
			}
			if !reflect.DeepEqual(schema, sanitized) {
				tool[key] = sanitized
				changed = true
				schemasChanged++
			}
		}
	}
	if !changed {
		return nil, nil
	}
	if r.report && schemasChanged > 0 {
		r.logger.Printf("rewrote %d tool schemas", schemasChanged)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func parseMessageLine(line []byte) (map[string]json.RawMessage, []byte) {
	body := line
	var ending []byte
	if len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
		ending = []byte("\n")
		if len(body) > 0 && body[len(body)-1] == '\r' {
			body = body[:len(body)-1]
			ending = []byte("\r\n")
		}
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(body, &message); err != nil || message == nil {
		return nil, nil
	}
	return message, ending
}

func marshalMessageLine(message map[string]json.RawMessage, ending, original []byte) []byte {
	encoded, err := json.Marshal(message)
	if err != nil {
		return original
	}
	return append(encoded, ending...)
}

func rawString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, false
	}
	return value, true
}

func messageID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	switch value.(type) {
	case string, json.Number:
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return "", false
		}
		return compact.String(), true
	default:
		return "", false
	}
}

var invalidToolNameCharacters = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
var repeatedUnderscores = regexp.MustCompile(`_+`)

func sanitizeToolName(name string) string {
	name = invalidToolNameCharacters.ReplaceAllString(name, "_")
	return repeatedUnderscores.ReplaceAllString(name, "_")
}
