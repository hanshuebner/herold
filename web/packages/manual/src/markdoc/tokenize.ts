/**
 * Minimal syntax tokenizer for code blocks.
 *
 * Supports: toml, bash/sh/shell, go, json, text.
 * Deliberately small — no dependency on shiki, prism, or highlight.js.
 *
 * Returns an array of tokens, each with a `type` (CSS class suffix) and
 * `text` (the raw text).  The caller renders them as <span class="tok tok--{type}">.
 *
 * Unknown languages fall back to a single "text" token for the entire input.
 */

export interface Token {
  type:
    | 'keyword'
    | 'string'
    | 'comment'
    | 'number'
    | 'operator'
    | 'punct'
    | 'text'
    | 'key'
    | 'value'
    | 'section';
  text: string;
}

type Rule = { re: RegExp; type: Token['type'] };

function buildLexer(rules: Rule[]): (src: string) => Token[] {
  const combined = new RegExp(
    rules.map((r) => `(${r.re.source})`).join('|'),
    'g',
  );
  return function lex(src: string): Token[] {
    const tokens: Token[] = [];
    let lastIndex = 0;
    let match: RegExpExecArray | null;
    combined.lastIndex = 0;
    while ((match = combined.exec(src)) !== null) {
      if (match.index > lastIndex) {
        tokens.push({ type: 'text', text: src.slice(lastIndex, match.index) });
      }
      for (let i = 0; i < rules.length; i++) {
        const groupIndex = i + 1;
        if (match[groupIndex] !== undefined) {
          const rule = rules[i];
          if (rule !== undefined) {
            tokens.push({ type: rule.type, text: match[0] });
          }
          break;
        }
      }
      lastIndex = combined.lastIndex;
    }
    if (lastIndex < src.length) {
      tokens.push({ type: 'text', text: src.slice(lastIndex) });
    }
    return tokens;
  };
}

/* ── TOML ──────────────────────────────────────────────────────────────── */

const tomlLexer = buildLexer([
  { re: /#[^\n]*/, type: 'comment' },
  { re: /\[\[[^\]]*\]\]/, type: 'section' },
  { re: /\[[^\]]*\]/, type: 'section' },
  {
    re: /"""[\s\S]*?"""|'''[\s\S]*?'''|"(?:[^"\\]|\\.)*"|'[^']*'/,
    type: 'string',
  },
  { re: /\b(?:true|false)\b/, type: 'keyword' },
  { re: /[-+]?(?:\d+_?)*\d+(?:\.\d+)?(?:[eE][-+]?\d+)?/, type: 'number' },
  { re: /[a-zA-Z_][\w.-]*(?=\s*=)/, type: 'key' },
  { re: /[=,\[\]{}]/, type: 'punct' },
]);

/* ── Bash / shell ─────────────────────────────────────────────────────── */

const bashKeywords = /\b(?:if|then|else|elif|fi|for|while|do|done|case|esac|in|function|return|export|local|readonly|declare|source|set|unset|shift|break|continue|exit)\b/;

const bashLexer = buildLexer([
  { re: /#[^\n]*/, type: 'comment' },
  { re: /"(?:[^"\\]|\\.)*"|'[^']*'/, type: 'string' },
  { re: bashKeywords, type: 'keyword' },
  { re: /\$(?:[a-zA-Z_]\w*|\{[^}]*\})/, type: 'value' },
  { re: /\b\d+\b/, type: 'number' },
  { re: /[|&;<>(){}\[\]]/, type: 'punct' },
]);

/* ── Go ────────────────────────────────────────────────────────────────── */

const goKeywords =
  /\b(?:break|case|chan|const|continue|default|defer|else|fallthrough|for|func|go|goto|if|import|interface|map|package|range|return|select|struct|switch|type|var)\b/;

const goLexer = buildLexer([
  { re: /\/\/[^\n]*/, type: 'comment' },
  { re: /\/\*[\s\S]*?\*\//, type: 'comment' },
  {
    re: /`[^`]*`|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/,
    type: 'string',
  },
  { re: goKeywords, type: 'keyword' },
  {
    re: /\b(?:true|false|nil|iota)\b/,
    type: 'keyword',
  },
  {
    re: /\b(?:bool|byte|complex64|complex128|error|float32|float64|int|int8|int16|int32|int64|rune|string|uint|uint8|uint16|uint32|uint64|uintptr)\b/,
    type: 'keyword',
  },
  {
    re: /\b0[xX][0-9a-fA-F]+|\b\d+(?:\.\d+)?(?:[eE][-+]?\d+)?\b/,
    type: 'number',
  },
  { re: /[:=!<>+\-*\/%&|^~]+/, type: 'operator' },
  { re: /[(){}\[\];,.]/, type: 'punct' },
]);

/* ── JSON ──────────────────────────────────────────────────────────────── */

const jsonLexer = buildLexer([
  { re: /"(?:[^"\\]|\\.)*"(?=\s*:)/, type: 'key' },
  { re: /"(?:[^"\\]|\\.)*"/, type: 'string' },
  { re: /\b(?:true|false|null)\b/, type: 'keyword' },
  { re: /-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?/, type: 'number' },
  { re: /[{}[\],:]/, type: 'punct' },
]);

/* ── Dispatch ──────────────────────────────────────────────────────────── */

const lexers: Record<string, (src: string) => Token[]> = {
  toml: tomlLexer,
  bash: bashLexer,
  sh: bashLexer,
  shell: bashLexer,
  go: goLexer,
  json: jsonLexer,
};

/**
 * Tokenize `source` for the given `lang`.
 *
 * Unknown languages return a single text token.  This function never
 * throws; bad input produces degenerate (text-only) output.
 */
export function tokenize(source: string, lang: string): Token[] {
  const lexer = lexers[lang.toLowerCase()];
  if (!lexer) {
    return [{ type: 'text', text: source }];
  }
  try {
    return lexer(source);
  } catch {
    return [{ type: 'text', text: source }];
  }
}
