package com.netzhansa.herold.android.ui.compose

import android.annotation.SuppressLint
import android.os.Looper
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.viewinterop.AndroidView
import com.netzhansa.herold.shared.compose.ComposeAttachment
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import java.io.ByteArrayInputStream

/** The formatting the toolbar offers (suite REQ-MAIL-05). */
enum class EditorCommand(val js: String) {
    BOLD("document.execCommand('bold')"),
    ITALIC("document.execCommand('italic')"),
    BULLET_LIST("document.execCommand('insertUnorderedList')"),
}

/**
 * The body editor: a `contenteditable` document in a WebView, which is the
 * same renderer the reading pane uses, so a quoted original looks in the
 * composer exactly as it looked in the thread.
 *
 * The document is built by the shared HTML pipeline
 * (`HtmlSanitizer.document` over `HtmlSanitizer.sanitize`), so the quoted
 * body arrives with its active elements already removed. JavaScript is on
 * here - `document.execCommand` is what applies the formatting - and the
 * page can reach exactly one bridge method, which hands the edited HTML
 * back; every network load other than an inline image served from memory
 * is refused.
 */
class EditorHandle {
    internal var webView: WebView? = null
    internal var onHtml: ((String) -> Unit)? = null

    /** Applies a formatting command to the selection. */
    fun run(command: EditorCommand) = evaluate(command.js + ";herold.publish();")

    /** Wraps the selection in a link. */
    fun link(url: String) {
        val escaped = url.replace("\\", "\\\\").replace("'", "\\'")
        evaluate("document.execCommand('createLink', false, '$escaped');herold.publish();")
    }

    /** Places an inline image at the cursor, referenced by its Content-ID. */
    fun insertInlineImage(cid: String) {
        val src = HtmlSanitizer.INLINE_SCHEME + cid
        evaluate("document.execCommand('insertHTML', false, '<img src=\"$src\">');herold.publish();")
    }

    /** Asks the editor to hand its current HTML to the state holder. */
    fun publish() = evaluate("herold.publish();")

    /**
     * A WebView may only be touched from the thread it was created on. An
     * upload's completion resumes on a worker, so the call is posted back.
     */
    private fun evaluate(script: String) {
        val view = webView ?: return
        if (Looper.myLooper() == Looper.getMainLooper()) {
            view.evaluateJavascript(script, null)
        } else {
            view.post { view.evaluateJavascript(script, null) }
        }
    }
}

@SuppressLint("SetJavaScriptEnabled")
@Composable
fun RichTextEditor(
    initialHtml: String,
    darkTheme: Boolean,
    attachments: List<ComposeAttachment>,
    handle: EditorHandle,
    onHtmlChanged: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    // The inline images the body references, resolvable while their bytes
    // are still in memory - the message has not been sent, so there is no
    // blob to download yet.
    val inlineBytes = remember(attachments) {
        attachments.filter { it.inline && it.cid != null && it.bytes != null }
            .associate { it.cid!! to (it.type to it.bytes!!) }
    }
    handle.onHtml = onHtmlChanged

    AndroidView(
        modifier = modifier,
        factory = { context ->
            WebView(context).apply {
                settings.javaScriptEnabled = true
                settings.allowFileAccess = false
                settings.allowContentAccess = false
                settings.domStorageEnabled = false
                addJavascriptInterface(
                    object {
                        @JavascriptInterface
                        fun changed(html: String) {
                            post { handle.onHtml?.invoke(html) }
                        }
                    },
                    BRIDGE,
                )
                webViewClient = object : WebViewClient() {
                    override fun shouldInterceptRequest(
                        view: WebView?,
                        request: WebResourceRequest?,
                    ): WebResourceResponse? {
                        val url = request?.url?.toString() ?: return null
                        if (url.startsWith(HtmlSanitizer.INLINE_SCHEME)) {
                            val cid = url.removePrefix(HtmlSanitizer.INLINE_SCHEME)
                            val resolved = inlineBytes[cid] ?: return blocked()
                            return WebResourceResponse(
                                resolved.first.substringBefore(';'),
                                null,
                                ByteArrayInputStream(resolved.second),
                            )
                        }
                        if (url.startsWith("data:")) return null
                        return blocked()
                    }

                    private fun blocked() =
                        WebResourceResponse("text/plain", "utf-8", ByteArrayInputStream(ByteArray(0)))
                }
                handle.webView = this
                loadDataWithBaseURL(null, editorDocument(initialHtml, darkTheme), "text/html", "utf-8", null)
            }
        },
        onRelease = { if (handle.webView === it) handle.webView = null },
    )
}

/**
 * The editable document. The cursor starts in the empty paragraph above
 * the quoted original, which is where a reply is written.
 */
private fun editorDocument(body: String, darkTheme: Boolean): String {
    val sanitised = HtmlSanitizer.sanitize(body, loadRemoteImages = false).html
    val background = if (darkTheme) "#1b1b1b" else "#ffffff"
    val foreground = if (darkTheme) "#e6e6e6" else "#1b1b1b"
    return """
        <!DOCTYPE html>
        <html><head>
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <style>
          body { margin: 0; background: $background; color: $foreground;
                 font-family: sans-serif; font-size: 15px; line-height: 1.45; }
          #editor { padding: 12px; min-height: 200px; outline: none; overflow-wrap: break-word; }
          #editor img { max-width: 100%; height: auto; }
          /* The empty paragraphs a reply opens with are where the answer is
             typed; give them a line's height so they can be tapped into. */
          #editor p:empty { min-height: 1.2em; }
          blockquote { margin: 0 0 0 8px; padding-left: 8px; border-left: 2px solid #888; color: #888; }
        </style>
        </head><body>
        <div id="editor" contenteditable="true">$sanitised</div>
        <script>
          var editor = document.getElementById('editor');
          var herold = {
            publish: function () { $BRIDGE.changed(editor.innerHTML); }
          };
          editor.addEventListener('input', herold.publish);
          editor.addEventListener('blur', herold.publish);
          (function () {
            var first = editor.firstChild;
            var range = document.createRange();
            range.setStart(first || editor, 0);
            range.collapse(true);
            var selection = window.getSelection();
            selection.removeAllRanges();
            selection.addRange(range);
          })();
        </script>
        </body></html>
    """.trimIndent()
}

private const val BRIDGE = "HeroldEditorBridge"
