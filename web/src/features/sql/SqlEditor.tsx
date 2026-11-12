import { useEffect, useRef } from "react";
import { EditorView, keymap, placeholder } from "@codemirror/view";
import { EditorState, Compartment, Prec } from "@codemirror/state";
import { basicSetup } from "codemirror";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { PostgreSQL, sql, type SQLNamespace } from "@codemirror/lang-sql";
import { clearErrors, runnableText } from "./editorUtil";

export interface SqlEditorProps {
  initial: string;
  schema: SQLNamespace | undefined;
  onChange: (sql: string) => void;
  onRun: (sql: string) => void;
  onCancel: () => void;
  onReady: (view: EditorView | null) => void;
}

const theme = EditorView.theme({
  "&": { height: "100%", backgroundColor: "var(--bg)", color: "var(--fg)" },
  ".cm-content": { caretColor: "var(--fg)" },
  ".cm-gutters": {
    backgroundColor: "var(--bg-soft)",
    color: "var(--fg-muted)",
    borderRight: "1px solid var(--bg1)",
  },
  ".cm-activeLine, .cm-activeLineGutter": {
    backgroundColor: "color-mix(in srgb, var(--bg1) 60%, transparent)",
  },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
    backgroundColor: "var(--selection) !important",
  },
  ".cm-tooltip": {
    backgroundColor: "var(--bg-soft)",
    border: "1px solid var(--bg2)",
    color: "var(--fg)",
  },
  ".cm-tooltip-autocomplete ul li[aria-selected]": {
    backgroundColor: "var(--accent)",
    color: "var(--bg)",
  },
  ".cm-scroller": { fontFamily: "inherit" },
});

// Gruvbox token colors via CSS variables, so light and dark both work.
const highlight = HighlightStyle.define([
  { tag: tags.keyword, color: "var(--red)", fontWeight: "600" },
  { tag: [tags.string, tags.special(tags.string)], color: "var(--green)" },
  { tag: [tags.number, tags.bool, tags.null], color: "var(--purple)" },
  { tag: tags.comment, color: "var(--fg-muted)", fontStyle: "italic" },
  { tag: [tags.typeName, tags.standard(tags.name)], color: "var(--yellow)" },
  { tag: [tags.function(tags.variableName), tags.special(tags.name)], color: "var(--aqua)" },
  { tag: tags.operator, color: "var(--orange)" },
  { tag: [tags.quote, tags.special(tags.variableName)], color: "var(--blue)" },
]);

export function SqlEditor(p: SqlEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const lang = useRef(new Compartment());
  const cb = useRef(p);
  const viewRef = useRef<EditorView | null>(null);
  useEffect(() => {
    cb.current = p;
  });

  useEffect(() => {
    const view = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: p.initial,
        extensions: [
          basicSetup,
          theme,
          syntaxHighlighting(highlight),
          placeholder("Write SQL… (⌘/Ctrl+Enter to run, Esc to cancel)"),
          lang.current.of(sql({ dialect: PostgreSQL, upperCaseKeywords: false })),
          Prec.highest(
            keymap.of([
              { key: "Mod-Enter", run: (v) => (cb.current.onRun(runnableText(v)), true) },
              { key: "Escape", run: () => (cb.current.onCancel(), false) },
            ]),
          ),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) {
              cb.current.onChange(u.state.doc.toString());
              if (u.view) clearErrors(u.view);
            }
          }),
        ],
      }),
    });
    viewRef.current = view;
    cb.current.onReady(view);
    view.focus();
    return () => {
      cb.current.onReady(null);
      viewRef.current = null;
      view.destroy();
    };
    // the editor is created once; props are read through cb
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const view = viewRef.current;
    if (!view || !p.schema) return;
    view.dispatch({
      effects: lang.current.reconfigure(
        sql({
          dialect: PostgreSQL,
          schema: p.schema,
          defaultSchema: "public",
          upperCaseKeywords: false,
        }),
      ),
    });
  }, [p.schema]);

  return <div className="sql-editor" ref={host} />;
}
