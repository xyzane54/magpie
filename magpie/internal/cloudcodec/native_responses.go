package cloudcodec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// nativeResponseCollector changes the output envelope, never item protocols.
// Identity and complete-item checks run before a terminal can be committed.
type nativeResponseCollector struct {
	created      Obj
	response     Obj
	items        map[int]Obj
	byID         map[string]int
	byCallID     map[string]int
	done         map[int]bool
	arguments    map[int]*strings.Builder
	argumentDone map[int]string
}

func sameNativeIdentity(a, b Obj) bool {
	for _, key := range []string{"id", "type", "name", "call_id", "namespace"} {
		if Str(a, key) != Str(b, key) {
			return false
		}
	}
	return true
}

func sameNativeJSON(a, b any) bool {
	decode := func(v any) any {
		var result any
		d := json.NewDecoder(bytes.NewReader(Raw(v)))
		d.UseNumber()
		if d.Decode(&result) != nil {
			return nil
		}
		return result
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

func nativeFunctionItem(item Obj) error {
	if Str(item, "type") != "function_call" {
		return nil
	}
	if Str(item, "call_id") == "" || Str(item, "name") == "" {
		return ErrCloudReply
	}
	var args string
	var object Obj
	if json.Unmarshal(item["arguments"], &args) != nil || json.Unmarshal([]byte(args), &object) != nil || object == nil {
		return ErrCloudReply
	}
	return nil
}

func nativeCompleteItem(item Obj) error {
	if Str(item, "type") == "custom_tool_call" {
		var input *string
		if Str(item, "call_id") == "" || Str(item, "name") == "" || json.Unmarshal(item["input"], &input) != nil || input == nil {
			return ErrCloudReply
		}
	}
	return nativeFunctionItem(item)
}

func validateNativeOutput(output []Obj) error {
	ids, calls := map[string]bool{}, map[string]bool{}
	for _, item := range output {
		if item == nil || nativeCompleteItem(item) != nil {
			return ErrCloudReply
		}
		if id := Str(item, "id"); id != "" {
			if ids[id] {
				return ErrCloudReply
			}
			ids[id] = true
		}
		if typ := Str(item, "type"); typ == "function_call" || typ == "custom_tool_call" {
			id := Str(item, "call_id")
			if id == "" || Str(item, "name") == "" || calls[id] {
				return ErrCloudReply
			}
			calls[id] = true
		}
	}
	return nil
}

func (c *nativeResponseCollector) add(data string) error {
	var event Obj
	json.Unmarshal([]byte(data), &event)
	typ := Str(event, "type")
	switch typ {
	case "response.created":
		if c.created != nil {
			return ErrCloudReply
		}
		json.Unmarshal(event["response"], &c.created)
	case "response.completed", "response.incomplete":
		json.Unmarshal(event["response"], &c.response)
		if c.created != nil && Str(c.created, "id") != Str(c.response, "id") {
			return ErrCloudReply
		}
		if typ == "response.completed" && Str(c.response, "status") != "completed" || typ == "response.incomplete" && Str(c.response, "status") != "incomplete" {
			return ErrCloudReply
		}
		_, err := c.whole()
		return err
	case "response.output_item.added", "response.output_item.done":
		var index int
		var item Obj
		if json.Unmarshal(event["output_index"], &index) != nil || index < 0 || index > CloudReplyLimit || json.Unmarshal(event["item"], &item) != nil || item == nil {
			return ErrCloudReply
		}
		if c.items == nil {
			c.items = map[int]Obj{}
			c.byID = map[string]int{}
			c.byCallID = map[string]int{}
			c.done = map[int]bool{}
			c.arguments = map[int]*strings.Builder{}
			c.argumentDone = map[int]string{}
		}
		if c.done[index] || typ == "response.output_item.added" && c.items[index] != nil {
			return ErrCloudReply
		}
		if old := c.items[index]; old != nil && !sameNativeIdentity(old, item) {
			return ErrCloudReply
		}
		if id := Str(item, "id"); id != "" {
			if previous, ok := c.byID[id]; ok && previous != index {
				return ErrCloudReply
			}
			c.byID[id] = index
		}
		if kind := Str(item, "type"); kind == "function_call" || kind == "custom_tool_call" {
			if id := Str(item, "call_id"); id != "" {
				if previous, ok := c.byCallID[id]; ok && previous != index {
					return ErrCloudReply
				}
				c.byCallID[id] = index
			}
		}
		if typ == "response.output_item.done" {
			if nativeCompleteItem(item) != nil {
				return ErrCloudReply
			}
			if args, ok := c.argumentDone[index]; ok && args != Str(item, "arguments") {
				return ErrCloudReply
			}
		}
		c.items[index] = item
		c.done[index] = typ == "response.output_item.done"
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		var index int
		if json.Unmarshal(event["output_index"], &index) != nil {
			var ok bool
			index, ok = c.byID[Str(event, "item_id")]
			if !ok {
				return ErrCloudReply
			}
		}
		item := c.items[index]
		if item == nil || c.done[index] || Str(item, "type") != "function_call" {
			return ErrCloudReply
		}
		if id := Str(event, "item_id"); id != "" && id != Str(item, "id") {
			return ErrCloudReply
		}
		if typ == "response.function_call_arguments.delta" {
			if _, done := c.argumentDone[index]; done {
				return ErrCloudReply
			}
			if c.arguments[index] == nil {
				c.arguments[index] = &strings.Builder{}
			}
			c.arguments[index].WriteString(Str(event, "delta"))
		} else {
			if _, done := c.argumentDone[index]; done {
				return ErrCloudReply
			}
			args := Str(event, "arguments")
			probe := Obj{"type": Raw("function_call"), "call_id": item["call_id"], "name": item["name"], "arguments": Raw(args)}
			if nativeFunctionItem(probe) != nil {
				return ErrCloudReply
			}
			c.argumentDone[index] = args
		}
	}
	return nil
}

func (c *nativeResponseCollector) whole() ([]byte, error) {
	if c.response == nil {
		return nil, ErrCloudReply
	}
	response := c.response
	for key, value := range c.created {
		if _, present := response[key]; !present {
			response[key] = value
		}
	}
	if _, present := response["object"]; !present {
		response["object"] = Raw("response")
	}
	var output []Obj
	if raw, present := response["output"]; present && json.Unmarshal(raw, &output) != nil {
		return nil, ErrCloudReply
	}
	if len(output) == 0 && len(c.items) > 0 {
		for i := 0; i < len(c.items); i++ {
			item := c.items[i]
			if item == nil {
				return nil, ErrCloudReply
			}
			if !c.done[i] {
				if Str(item, "type") != "function_call" {
					return nil, ErrCloudReply
				}
				args := Str(item, "arguments")
				if builder := c.arguments[i]; builder != nil {
					args = builder.String()
				}
				if full, ok := c.argumentDone[i]; ok {
					args = full
				}
				item["arguments"] = Raw(args)
				if nativeFunctionItem(item) != nil {
					return nil, ErrCloudReply
				}
				if Str(item, "status") == "in_progress" {
					item["status"] = Raw("completed")
				}
			}
			output = append(output, item)
		}
		response["output"] = Raw(output)
	} else {
		for index, item := range c.items {
			if index >= len(output) || !sameNativeIdentity(item, output[index]) {
				return nil, ErrCloudReply
			}
			if c.done[index] && !sameNativeJSON(item, output[index]) {
				return nil, ErrCloudReply
			}
			if args, ok := c.argumentDone[index]; ok && args != Str(output[index], "arguments") {
				return nil, ErrCloudReply
			}
		}
	}
	if validateNativeOutput(output) != nil {
		return nil, ErrCloudReply
	}
	body := Raw(response)
	if !CloudWholeComplete(Responses, body) || len(body) > CloudReplyLimit {
		return nil, ErrCloudReply
	}
	return body, nil
}
