package gateway

// Codex's Responses Lite, for the models its catalog marks so (the ChatGPT
// backend's gpt-6 and gpt-5.6 among them), sends no tools and no
// instructions: its tools go as the first input item, an additional_tools
// one, and its prompt as a developer message after it. Every plain
// function and freeform tool is grouped in a namespace named "functions",
// which Codex reads as no namespace at all when a call comes back. Only
// OpenAI knows the item — xAI's backend turns the whole request away over
// it (#350: 422 "unknown item type additional_tools") — so a request going
// anywhere else has its tools in the tools, the "functions" namespace
// opened into them, as Codex sends a model that isn't Lite.
