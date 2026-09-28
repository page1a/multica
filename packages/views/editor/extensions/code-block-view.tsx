"use client";

import { useState } from "react";
import { NodeViewWrapper, NodeViewContent } from "@tiptap/react";
import type { NodeViewProps } from "@tiptap/react";
import { Code as CodeIcon, Copy, Check, Eye } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { Dialog, DialogContent } from "@multica/ui/components/ui/dialog";
import { useDebouncedValue } from "../../common/use-debounced-value";
import { useT } from "../../i18n";
import { MermaidDiagram } from "../mermaid-diagram";
import { CodeBlockIframe } from "../code-block-iframe";
import { PREVIEW_POSTER_HEIGHT, PreviewPoster } from "../preview-poster";
import { useBackToDismiss } from "../../navigation";

// Coalesces fast keystrokes before re-rendering live previews.
// `mermaid.initialize()` mutates a process-global config, so back-to-back
// renders during typing can race a concurrent ReadonlyContent render
// (e.g. a comment card) and clobber its theme variables. 200ms keeps the
// "live preview" feel while making concurrent inits unlikely in practice.
// HTML preview reuses the same debounce: re-keying iframe.srcDoc on every
// keystroke causes the iframe to re-load and flicker.
const PREVIEW_DEBOUNCE_MS = 200;

const HTML_PREVIEW_HEIGHT = "h-[480px]";

function CodeBlockView({ node }: NodeViewProps) {
  const { t } = useT("editor");
  const [copied, setCopied] = useState(false);
  // HTML blocks default to "preview"; the user can flip to "source" to
  // edit the markup directly. Note: the source `<pre>` MUST stay mounted
  // (just hidden) so ProseMirror keeps its NodeView bindings — unmounting
  // it would break editing.
  const [view, setView] = useState<"preview" | "source">("preview");
  // Phones get a short, non-scrollable thumbnail that opens the full-screen
  // preview on tap (see PreviewPoster); the back gesture closes it again.
  const isMobile = useIsMobile();
  const [fullscreen, setFullscreen] = useState(false);
  useBackToDismiss(isMobile && fullscreen, () => setFullscreen(false));
  const language = node.attrs.language || "";
  const isMermaid = language === "mermaid";
  const isHtml = language === "html";
  const chart = node.textContent;
  const debouncedChart = useDebouncedValue(
    isMermaid ? chart : "",
    PREVIEW_DEBOUNCE_MS,
  );
  const debouncedHtml = useDebouncedValue(
    isHtml ? chart : "",
    PREVIEW_DEBOUNCE_MS,
  );

  const handleCopy = async () => {
    const text = node.textContent;
    if (!text) return;
    if (await copyText(text)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const showHtmlPreview = isHtml && view === "preview";
  const toggleView = () =>
    setView((v) => (v === "preview" ? "source" : "preview"));

  return (
    <NodeViewWrapper className="code-block-wrapper group/code relative my-3">
      {isMermaid && debouncedChart.trim() && (
        <div
          contentEditable={false}
          className="mermaid-diagram-preview mb-1"
        >
          <MermaidDiagram chart={debouncedChart} />
        </div>
      )}
      {isHtml && showHtmlPreview && (
        // CSS-hidden when toggled off so the `<pre>` below stays mounted —
        // unmounting either side would either lose ProseMirror bindings
        // (source) or thrash iframe.srcDoc (preview).
        <div contentEditable={false} className="relative mb-1">
          <CodeBlockIframe
            html={debouncedHtml}
            title={t(($) => $.code_block.html_preview)}
            heightClassName={isMobile ? PREVIEW_POSTER_HEIGHT : HTML_PREVIEW_HEIGHT}
            className={isMobile ? "pointer-events-none" : undefined}
          />
          {isMobile && <PreviewPoster onOpen={() => setFullscreen(true)} />}
          <Dialog open={fullscreen} onOpenChange={setFullscreen}>
            <DialogContent
              className="!max-w-6xl !h-[min(90dvh,calc(100dvh-2rem))] w-full p-0 gap-0 overflow-hidden"
              aria-label={t(($) => $.code_block.fullscreen)}
            >
              <CodeBlockIframe
                html={debouncedHtml}
                title={t(($) => $.code_block.html_preview)}
                heightClassName="h-full"
                className="rounded-none border-0"
              />
            </DialogContent>
          </Dialog>
        </div>
      )}
      <div
        contentEditable={false}
        className="code-block-header absolute top-0 right-0 z-10 flex items-center gap-1.5 px-2 py-1.5 transition-opacity group-hover/code:opacity-100 focus-within:opacity-100 [@media(hover:hover)]:opacity-0 [@media(hover:none)]:rounded-bl-md [@media(hover:none)]:bg-muted"
      >
        {language && (
          <span className="text-caption text-muted-foreground select-none">
            {language}
          </span>
        )}
        {isHtml && (
          <button
            type="button"
            onClick={toggleView}
            className="flex h-6 w-6 items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
            title={
              view === "preview"
                ? t(($) => $.code_block.show_source)
                : t(($) => $.code_block.show_preview)
            }
            aria-label={
              view === "preview"
                ? t(($) => $.code_block.show_source)
                : t(($) => $.code_block.show_preview)
            }
          >
            {view === "preview" ? (
              <CodeIcon className="h-3.5 w-3.5" />
            ) : (
              <Eye className="h-3.5 w-3.5" />
            )}
          </button>
        )}
        <button
          type="button"
          onClick={handleCopy}
          className="flex h-6 w-6 items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
          title={t(($) => $.code_block.copy_code)}
          aria-label={t(($) => $.code_block.copy_code)}
        >
          {copied ? (
            <Check className="h-3.5 w-3.5" />
          ) : (
            <Copy className="h-3.5 w-3.5" />
          )}
        </button>
      </div>
      {/* `<pre>` + NodeViewContent must remain mounted so the user can keep
          editing the code block contents. When the HTML preview is showing
          we just visually hide it — ProseMirror still tracks it. */}
      <pre
        spellCheck={false}
        className={cn(showHtmlPreview && "sr-only")}
        aria-hidden={showHtmlPreview ? "true" : undefined}
      >
        {/* @ts-expect-error -- NodeViewContent supports as="code" at runtime */}
        <NodeViewContent as="code" />
      </pre>
    </NodeViewWrapper>
  );
}

export { CodeBlockView };
