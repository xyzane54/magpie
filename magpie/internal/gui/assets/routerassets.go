// Package assets shares Magpie's original visual resources without importing
// its GUI backend, desktop host, plugin registry or startup lifecycle.
package assets

import "embed"

// Router contains only passive resources used by the trimmed client. In
// particular, app.js, boot.js and plugin scripts are not part of this set.
//
//go:embed app.css context.css i18n.js plugins/routing/routing.css icons/*.svg icons/loomy.png
var Router embed.FS
