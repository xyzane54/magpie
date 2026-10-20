package routerledger

// ReferencePriceSafe validates a snapshot before admission. A legacy Profile
// can contain an unsafe descriptive source; callers should omit that price and
// report missing observation rather than dropping the entire business record.
func ReferencePriceSafe(p *ReferencePrice) bool { return validReferencePrice(p) }

// CallSafe lets the nonblocking consumer validate before using any event to
// enrich a business result. The store repeats this validation before writing.
func CallSafe(c Call) bool { return validCall(c) }
