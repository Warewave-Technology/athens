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
  "&": { height: "100%", backgroundColor: "var(--surface)", color: "var(--text)" },
  ".cm-content": { caretColor: "var(--accent)" },
  ".cm-gutters": {
    backgroundColor: "var(--surface)",
    color: "var(--text-3)",
    borderRight: "1px solid var(--line)",
  },
  ".cm-activeLine, .cm-activeLineGutter": {
    backgroundColor: "color-mix(in srgb, var(--subtle) 55%, transparent)",
  },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
    backgroundColor: "var(--selection) !important",
  },
  ".cm-tooltip": {
    backgroundColor: "var(--surface)",
    border: "1px solid var(--line-strong)",
    borderRadius: "8px",
    boxShadow: "var(--shadow-pop)",
    color: "var(--text)",
  },
  ".cm-tooltip-autocomplete ul li[aria-selected]": {
    backgroundColor: "var(--accent-soft)",
    color: "var(--accent-text)",
  },
  ".cm-scroller": { fontFamily: "inherit" },
});

// Aegean token colors via CSS variables, so light and dark both work.
const highlight = HighlightStyle.define([
  { tag: tags.keyword, color: "var(--syn-keyword)", fontWeight: "500" },
  { tag: [tags.string, tags.special(tags.string)], color: "var(--syn-string)" },
  { tag: [tags.number, tags.bool, tags.null], color: "var(--syn-number)" },
  { tag: tags.comment, color: "var(--syn-comment)", fontStyle: "italic" },
  { tag: [tags.typeName, tags.standard(tags.name)], color: "var(--syn-type)" },
  { tag: [tags.function(tags.variableName), tags.special(tags.name)], color: "var(--syn-fn)" },
  { tag: tags.operator, color: "var(--text-2)" },
  { tag: [tags.quote, tags.special(tags.variableName)], color: "var(--syn-fn)" },
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
