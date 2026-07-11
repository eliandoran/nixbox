// nixbox Nix editor — CodeMirror 6, syntax highlighting only (no
// completion yet; the extension slot is left open for it later).
//
// Progressive enhancement: the server renders a plain
// `<textarea name="content" class="editor">`. This bundle turns each
// one into a CodeMirror view, hides the textarea, and mirrors every
// doc change back into it. That keeps HTMX form submission and the
// existing unsaved-changes guard in app.js (which listens for the
// textarea's `input` event) working with no changes. If this bundle
// fails to load, the plain textarea still works.
import { EditorState } from "@codemirror/state";
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightActiveLineGutter,
  drawSelection,
} from "@codemirror/view";
import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
} from "@codemirror/commands";
import { autocompletion } from "@codemirror/autocomplete";
import {
  syntaxHighlighting,
  HighlightStyle,
  indentOnInput,
  indentUnit,
  bracketMatching,
} from "@codemirror/language";
import { tags as t } from "@lezer/highlight";
import { nix } from "@replit/codemirror-lang-nix";

// Highlight colours are CSS variables (defined in style.css via
// light-dark()), so the editor follows the app's light/dark theme with
// zero JS coupling to the theme toggle — no Compartment reconfigure.
const highlightStyle = HighlightStyle.define([
  { tag: [t.keyword, t.moduleKeyword, t.operatorKeyword, t.controlKeyword], color: "var(--syntax-keyword)" },
  { tag: [t.definitionKeyword, t.definition(t.variableName)], color: "var(--syntax-def)" },
  { tag: [t.string, t.special(t.string), t.regexp], color: "var(--syntax-string)" },
  { tag: t.url, color: "var(--syntax-string)", textDecoration: "underline" },
  { tag: [t.comment, t.lineComment, t.blockComment], color: "var(--syntax-comment)", fontStyle: "italic" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--syntax-number)" },
  { tag: [t.variableName, t.propertyName, t.attributeName], color: "var(--syntax-name)" },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.function(t.definition(t.variableName))], color: "var(--syntax-func)" },
  { tag: [t.operator, t.punctuation, t.separator, t.bracket, t.brace, t.paren], color: "var(--syntax-punct)" },
  { tag: [t.escape, t.special(t.brace)], color: "var(--syntax-escape)" },
  { tag: t.invalid, color: "var(--bad)" },
]);

// Chrome (layout + neutrals) via CSS variables too.
const theme = EditorView.theme({
  "&": {
    color: "var(--text)",
    backgroundColor: "var(--surface)",
    border: "1px solid var(--border)",
    borderRadius: "10px",
    fontSize: "0.85rem",
    minHeight: "20rem",
    maxHeight: "70vh",
  },
  "&.cm-focused": { outline: "none", borderColor: "var(--accent)" },
  ".cm-scroller": {
    fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
    lineHeight: "1.5",
    borderRadius: "10px",
  },
  ".cm-content": { padding: "0.9rem 0", caretColor: "var(--text)" },
  ".cm-gutters": { backgroundColor: "var(--surface)", color: "var(--muted)", border: "none" },
  // drawSelection() paints the selection in a layer CM stacks *behind* the
  // content, so an opaque active-line fill would hide it. Keep the line fill
  // translucent so the selection shows through on the active line; the gutter
  // stays opaque as the strong current-line marker.
  ".cm-activeLine": { backgroundColor: "var(--editor-active-line)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--surface-2)", color: "var(--text)" },
  "&.cm-focused .cm-cursor": { borderLeftColor: "var(--text)" },
  // CM's drawSelection base theme sets a brighter focused background that
  // outranks a plain rule, so override with !important.
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
    background: "var(--syntax-selection) !important",
  },
  ".cm-content ::selection": { background: "var(--syntax-selection)" },
  ".cm-matchingBracket, &.cm-focused .cm-matchingBracket": {
    backgroundColor: "transparent",
    outline: "1px solid var(--muted)",
  },
  // Completion popup — CM's default tooltip is light; theme it to match.
  ".cm-tooltip": {
    backgroundColor: "var(--surface)",
    border: "1px solid var(--border)",
    borderRadius: "8px",
    color: "var(--text)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul": {
    fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
    fontSize: "0.82rem",
    maxHeight: "16rem",
  },
  ".cm-tooltip-autocomplete ul li[aria-selected]": {
    backgroundColor: "var(--accent)",
    color: "var(--on-accent)",
  },
  ".cm-completionIcon": { color: "var(--muted)", opacity: "0.8" },
  ".cm-completionDetail": { color: "var(--muted)", fontStyle: "italic" },
});

export function mount(textarea) {
  if (!(textarea instanceof HTMLTextAreaElement) || textarea.dataset.cmMounted) return;
  textarea.dataset.cmMounted = "1";

  // Mirror the doc into the (hidden) textarea and re-fire `input` so the
  // form submits the current text and the unsaved-changes guard updates.
  const sync = EditorView.updateListener.of((update) => {
    if (!update.docChanged) return;
    textarea.value = update.state.doc.toString();
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  });

  const saveKey = keymap.of([
    { key: "Mod-s", preventDefault: true, run: () => (textarea.form?.requestSubmit(), true) },
  ]);

  const view = new EditorView({
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        lineNumbers(),
        highlightActiveLine(),
        highlightActiveLineGutter(),
        history(),
        drawSelection(),
        indentOnInput(),
        bracketMatching(),
        indentUnit.of("  "),
        EditorState.tabSize.of(2),
        keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
        saveKey,
        nix(),
        // Completion for Nix keywords, builtins, and snippets, supplied by
        // the grammar via languageData; autocompletion() just enables the UI
        // (Ctrl-Space to open; Enter/Tab to accept). No option/package
        // completion yet — that needs a server-side data source (Tier 1).
        autocompletion(),
        syntaxHighlighting(highlightStyle),
        theme,
        sync,
      ],
    }),
  });

  textarea.style.display = "none";
  textarea.after(view.dom);
  return view;
}

function init() {
  for (const ta of document.querySelectorAll("textarea.editor")) mount(ta);
}

// `defer` runs this after parsing but before DOMContentLoaded, so the
// textarea already exists and CM mounts before app.js sets its guard
// baseline. Fall back to the event if somehow loaded earlier.
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
else init();

window.NixEditor = { mount };
