package cloudcodec

// openaiItemPrefix is the id prefix OpenAI takes for each kind of call item
// a vendor's reply may have handed Codex with another: magpie, or the
// vendor, gave a tool search's and a custom tool's call a function_call's
// id (fc_…), and Codex hands the item back on every later turn. OpenAI's
// own models and the ChatGPT backend turn the whole request away ("Invalid
// 'input[98].id': 'fc_…'. Expected an ID that begins with 'tsc'", "…with
// 'ctc'"), Codex's compaction with it.
var OpenaiItemPrefix = map[string]string{
	"tool_search_call": "tsc_",
	"custom_tool_call": "ctc_",
}
