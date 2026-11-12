import type { EditorView } from "@codemirror/view";
import { setDiagnostics } from "@codemirror/lint";

/** Text to run: the selection if there is one, else the whole document. */
export function runnableText(view: EditorView): string {
  const sel = view.state.selection.main;
  return sel.empty ? view.state.doc.toString() : view.state.sliceDoc(sel.from, sel.to);
}

/** Marks a 1-based character position as an error in the editor. */
export function markError(view: EditorView, position: number, message: string, base = 0) {
  const doc = view.state.doc;
  let from = Math.max(0, Math.min(doc.length, base + position - 1));
  // positions are in characters; CodeMirror uses UTF-16 offsets
  const text = doc.toString();
  let chars = 0;
  for (let i = base; i < text.length; i++) {
    if (chars === position - 1) {
      from = i;
      break;
    }
    const code = text.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff) i++;
    chars++;
  }
  const word = /^[\w$."]+/.exec(text.slice(from))?.[0].length ?? 1;
  view.dispatch(
    setDiagnostics(view.state, [
      { from, to: Math.min(doc.length, from + Math.max(1, word)), severity: "error", message },
    ]),
  );
}

export function clearErrors(view: EditorView) {
  view.dispatch(setDiagnostics(view.state, []));
}
