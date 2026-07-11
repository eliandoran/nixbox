// Bundle entry (esbuild → web/static/app.js; see web/embed.go). Each
// module wires its own document-level listeners at import time and
// activates only when its markup is present, so the one bundle serves
// every page and keeps working inside HTMX-swapped fragments. Import
// order mirrors the pre-split app.js so listener registration order —
// and therefore same-target dispatch order — is unchanged.
import "./jobs";
import "./journal";
import "./metrics";
import "./tabs";
import "./workload-form";
import "./dialogs";
import "./new-workload";
import "./chrome";
