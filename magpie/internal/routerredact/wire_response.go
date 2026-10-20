package routerredact

// RestoreResponse restores only protocol text and decoded tool argument string
// values. IDs, names, numbers, schema trees, errors, stop reasons and usage are
// kept as received. The caller converts the cloud response to the source
// protocol before invoking this method.
func (s *Snapshot) RestoreResponse(p Protocol, body []byte) ([]byte, error) {
	if s == nil {
		return nil, fail("redaction_released")
	}
	if _, err := s.RestoreText(""); err != nil {
		return nil, err
	}
	v, err := wireDecode(body, 32<<20)
	if err != nil {
		return nil, err
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, fail("wire_invalid")
	}
	d := &textDocument{root: o, byteLimit: 32 << 20}
	switch p {
	case Chat:
		if choices, exists := o["choices"]; exists {
			a, ok := choices.([]any)
			if !ok {
				return nil, fail("wire_invalid")
			}
			for _, value := range a {
				choice, ok := value.(map[string]any)
				if !ok {
					return nil, fail("wire_invalid")
				}
				m, ok := choice["message"].(map[string]any)
				if !ok {
					return nil, fail("wire_invalid")
				}
				if err := d.content(m, "content", Chat); err != nil {
					return nil, err
				}
				for _, key := range []string{"refusal", "reasoning_content", "reasoning", "argumentsDisplayText"} {
					if err := d.field(m, key); err != nil {
						return nil, err
					}
				}
				if calls, exists := m["tool_calls"]; exists {
					a, ok := calls.([]any)
					if !ok {
						return nil, fail("wire_invalid")
					}
					for _, value := range a {
						call, ok := value.(map[string]any)
						if !ok {
							return nil, fail("wire_invalid")
						}
						function, ok := call["function"].(map[string]any)
						if !ok {
							return nil, fail("wire_invalid")
						}
						if err := d.arguments(function, "arguments"); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	case Responses:
		if err := d.field(o, "output_text"); err != nil {
			return nil, err
		}
		if output, exists := o["output"]; exists {
			a, ok := output.([]any)
			if !ok {
				return nil, fail("wire_invalid")
			}
			for _, value := range a {
				item, ok := value.(map[string]any)
				if !ok {
					return nil, fail("wire_invalid")
				}
				if err := d.responseItem(item, false); err != nil {
					return nil, err
				}
			}
		}
	case Anthropic:
		if err := d.content(o, "content", Anthropic); err != nil {
			return nil, err
		}
	default:
		return nil, fail("wire_unsupported")
	}
	if d.err != nil {
		return nil, d.err
	}
	restored := make([]string, len(d.texts))
	protected := make(map[int]bool, len(d.protected))
	for _, index := range d.protected {
		protected[index] = true
	}
	total := 0
	for i, text := range d.texts {
		if protected[i] {
			restored[i] = text
		} else {
			restored[i], err = s.RestoreText(text)
			if err != nil {
				return nil, err
			}
		}
		total += len(restored[i])
		if total > 32<<20 {
			return nil, fail("wire_response_limit")
		}
	}
	return d.render(restored)
}
