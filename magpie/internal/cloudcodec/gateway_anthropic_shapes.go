package cloudcodec

// toolLoadedText is what a model is told of a tool ToolSearch loaded, where
// the tool_reference that names it can't go as it is.
func ToolLoadedText(name string) string {
	return "Tool " + name + " is loaded and can be called now."
}
