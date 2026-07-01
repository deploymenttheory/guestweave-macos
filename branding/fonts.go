// Package branding embeds weave's brand assets (fonts, artwork) so they travel
// inside the binary and can be loaded in-memory — without installing anything
// into the system or user font library.
package branding

import _ "embed"

// PlusJakartaSansMedium is the Plus Jakarta Sans Medium (weight 500) TrueType
// face used for the "guestweave" wordmark. It is loaded as an in-memory NSFont at
// runtime (via a CoreText font descriptor), never registered with the font
// manager, so it is available only to this process for drawing and is not
// installed into the system or user font library.
//
//go:embed fonts/Plus_Jakarta_Sans/static/PlusJakartaSans-Medium.ttf
var PlusJakartaSansMedium []byte
