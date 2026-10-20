package provider

import "github.com/yetone/magpie/internal/cloudcodec"

// Compatibility names delegate to the single shared wire implementation.
const Anthropic = cloudcodec.Anthropic
const BlockedHint = cloudcodec.BlockedHint
const Chat = cloudcodec.Chat
const CommandCodeGoHint = cloudcodec.CommandCodeGoHint
const CommandCodePlanID = cloudcodec.CommandCodePlanID
const Gemini = cloudcodec.Gemini
const Responses = cloudcodec.Responses
const verifyAdvice = cloudcodec.VerifyAdvice

type Protocol = cloudcodec.Protocol

var APIError = cloudcodec.APIError
var FlatName = cloudcodec.FlatName
var ObjectRoot = cloudcodec.ObjectRoot
var Verification = cloudcodec.Verification
var VerifyMessage = cloudcodec.VerifyMessage
var apiError = cloudcodec.ApiError
var appendDistinct = cloudcodec.AppendDistinct
var grokRef = cloudcodec.GrokRef
var nestedUnion = cloudcodec.NestedUnion
var objectRootIn = cloudcodec.ObjectRootIn
