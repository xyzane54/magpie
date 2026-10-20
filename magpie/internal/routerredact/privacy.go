package routerredact

func (*Pipeline) String() string               { return "redaction pipeline (private)" }
func (*Pipeline) GoString() string             { return "redaction pipeline (private)" }
func (*Pipeline) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }

// These types cross integration seams. Accidental formatting/JSON diagnostics
// reveal only a fixed label; explicit Body/RestoreText access remains private.
func (Input) String() string                    { return "redaction input (private)" }
func (Input) GoString() string                  { return "redaction input (private)" }
func (Extraction) String() string               { return "redaction extraction (private)" }
func (Extraction) GoString() string             { return "redaction extraction (private)" }
func (Entity) String() string                   { return "redaction entity (private)" }
func (Entity) GoString() string                 { return "redaction entity (private)" }
func (Result) String() string                   { return "redaction result (private)" }
func (Result) GoString() string                 { return "redaction result (private)" }
func (*Snapshot) String() string                { return "redaction snapshot (private)" }
func (*Snapshot) GoString() string              { return "redaction snapshot (private)" }
func (*Prepared) String() string                { return "redaction request (private)" }
func (*Prepared) GoString() string              { return "redaction request (private)" }
func (Input) MarshalJSON() ([]byte, error)      { return []byte(`{"private":true}`), nil }
func (Extraction) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }
func (Entity) MarshalJSON() ([]byte, error)     { return []byte(`{"private":true}`), nil }
func (Result) MarshalJSON() ([]byte, error)     { return []byte(`{"private":true}`), nil }
func (*Snapshot) MarshalJSON() ([]byte, error)  { return []byte(`{"private":true}`), nil }
func (*Prepared) MarshalJSON() ([]byte, error)  { return []byte(`{"private":true}`), nil }
