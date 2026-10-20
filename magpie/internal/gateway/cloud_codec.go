package gateway

import "github.com/yetone/magpie/internal/cloudcodec"

// CloudCodec and CloudReply retain the legacy adapter API. Their implementation
// lives with the shared pure protocol codecs, outside the gateway lifecycle.
type CloudCodec = cloudcodec.CloudCodec
type CloudReply = cloudcodec.CloudReply

var NewCloudCodec = cloudcodec.NewCloudCodec
