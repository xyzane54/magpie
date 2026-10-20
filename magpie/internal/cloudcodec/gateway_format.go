package cloudcodec

import (
	"encoding/json"
)

// Format is the shape a client asked the answer to come in (structured
// output), read from whichever API it spoke: Responses' text.format,
// Chat's response_format, Anthropic's output_config.format, Gemini's
// generationConfig.responseMimeType with its schema. Each API it is built
// for again asks for it in its own words; one that can't is told it in
// the system prompt (inSystem).
type Format struct {
	// Type is "json_schema" (Schema holds the schema the answer fits) or
	// "json_object" (any JSON object).
	Type string
	// Name and Description are json_schema's, as OpenAI's APIs take them;
	// Name is "response" where the client's API has none.
	Name, Description string
	Schema            json.RawMessage
	// Strict is the client's strict, nil when it said none.
	Strict *bool
	// told is a JSON object asked for in place of a schema the system
	// prompt already holds (asObject).
	told bool
}

// formatName is the name a schema goes under when the client's API names
// none (Anthropic's, Gemini's): OpenAI requires one.
const FormatName = "response"

// openAIFormat reads the format OpenAI's APIs ask for, in Responses'
// text.format ({type, name, schema, strict, description}) or Chat's
// response_format ({type, json_schema: {name, schema, strict,
// description}}). nil for {type: "text"}, the default, and for anything
// else.
func OpenAIFormat(raw json.RawMessage) *Format {
	var f struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Strict      *bool           `json:"strict"`
		JSONSchema  *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	switch f.Type {
	case "json_object":
		return &Format{Type: "json_object"}
	case "json_schema":
		if js := f.JSONSchema; js != nil {
			f.Name, f.Description, f.Schema, f.Strict = js.Name, js.Description, js.Schema, js.Strict
		}
		if len(SentRaw(f.Schema)) == 0 {
			// a schema of no shape is any JSON object
			return &Format{Type: "json_object"}
		}
		if f.Name == "" {
			f.Name = FormatName
		}
		return &Format{Type: "json_schema", Name: f.Name, Description: f.Description, Schema: f.Schema, Strict: f.Strict}
	}
	return nil
}

// schema is the JSON schema the answer has to fit, nil when there is none
// (no format, or any JSON object).
func (f *Format) SchemaValue() json.RawMessage {
	if f == nil || f.Type != "json_schema" {
		return nil
	}
	return f.Schema
}

// responses is the format as Responses' text.format says it.
func (f *Format) responses() map[string]any {
	if f.Type != "json_schema" {
		return map[string]any{"type": "json_object"}
	}
	out := map[string]any{"type": "json_schema", "name": f.Name, "schema": f.Schema}
	if f.Description != "" {
		out["description"] = f.Description
	}
	if f.Strict != nil {
		out["strict"] = *f.Strict
	}
	return out
}

// chat is the format as Chat's response_format says it.
func (f *Format) chat() map[string]any {
	if f.Type != "json_schema" {
		return map[string]any{"type": "json_object"}
	}
	js := map[string]any{"name": f.Name, "schema": f.Schema}
	if f.Description != "" {
		js["description"] = f.Description
	}
	if f.Strict != nil {
		js["strict"] = *f.Strict
	}
	return map[string]any{"type": "json_schema", "json_schema": js}
}

// instruction is the format said in words, for a model whose API can't be
// asked for it: Anthropic's Messages for any JSON object (its
// output_config.format takes a schema only), a bridge to a vendor's own
// API, or an upstream that turned the field away (formatRefused).
func (f *Format) Instruction() string {
	if s := f.SchemaValue(); len(s) > 0 {
		return "Respond with a single JSON value and nothing else, matching this JSON schema:\n" + string(s) + "."
	}
	return "Respond with a single JSON object and nothing else."
}

// inSystem is r with its format told in the system prompt instead of
// asked for in a field, r itself when it has none.
func (r *Request) InSystem() *Request {
	if r.Format == nil {
		return r
	}
	q := *r
	q.Format, q.FormatTold = nil, true
	if !r.Format.told {
		q.System = WithLine(q.System, r.Format.Instruction())
	}
	return &q
}

// asObject is r asking for any JSON object, its schema told in the system
// prompt; r itself when it asks for no schema.
func (r *Request) AsObject() *Request {
	if r.Format == nil || r.Format.Type != "json_schema" {
		return r
	}
	q := *r
	q.Format = &Format{Type: "json_object", told: true}
	q.System = WithLine(q.System, r.Format.Instruction())
	return &q
}

// withLine is a system prompt with s said at its end.
func WithLine(system, s string) string {
	if system != "" {
		system += "\n\n"
	}
	return system + s
}

// wantsJSON is whether the client asked for structured output, in a field
// or told in the system prompt.
func (r *Request) WantsJSON() bool { return r.Format != nil || r.FormatTold }
