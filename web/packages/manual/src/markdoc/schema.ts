/**
 * Markdoc tag schema for the Herold manual.
 *
 * Only the tags defined here are permitted in .mdoc source files.  Any
 * unknown tag causes the bundler to exit with a non-zero status.
 *
 * v1 tags:
 *   callout   -- informational / warning / caution box with optional title
 *   code-group -- tabbed code block grouping (children must be fenced code)
 *   include   -- verbatim file inclusion with syntax annotation
 *   req       -- requirement cross-reference (REQ-* ids)
 *   kbd       -- keyboard key sequence
 */
import type { Config, Schema } from '@markdoc/markdoc';
import Markdoc from '@markdoc/markdoc';

const { Tag } = Markdoc;

/** Callout box: type drives the visual variant (info / warning / caution). */
const callout: Schema = {
  render: 'Callout',
  children: ['paragraph', 'fence', 'list'],
  attributes: {
    type: {
      type: String,
      default: 'info',
      matches: ['info', 'warning', 'caution'],
      errorLevel: 'error',
    },
    title: {
      type: String,
    },
  },
  transform(node, config) {
    return new Tag(
      'Callout',
      {
        type: node.attributes['type'] as string,
        title: node.attributes['title'] as string | undefined,
      },
      node.transformChildren(config),
    );
  },
};

/** Code group: wraps multiple fenced code blocks in a tabbed UI. */
const codeGroup: Schema = {
  render: 'CodeGroup',
  children: ['fence'],
  attributes: {},
  transform(node, config) {
    return new Tag('CodeGroup', {}, node.transformChildren(config));
  },
};

/**
 * Include: embed an external file verbatim.
 *
 * `file` is a path relative to the content root.
 * `lang` annotates the code block language (for the tiny tokenizer).
 *
 * The bundler resolves and inlines the file content at bundle time.
 * At schema-validation time (validate.mjs) the tag is accepted; the
 * bundler validates that `file` exists.
 */
const include: Schema = {
  render: 'IncludedCode',
  selfClosing: true,
  attributes: {
    file: {
      type: String,
      required: true,
      errorLevel: 'error',
    },
    lang: {
      type: String,
      default: 'text',
    },
    // `content` is injected by the bundler at bundle time after reading
    // the referenced file from disk.  Declaring it here prevents Markdoc
    // from emitting a validation error when the attribute is present.
    content: {
      type: String,
    },
  },
  transform(node, _config) {
    return new Tag('IncludedCode', {
      file: node.attributes['file'] as string,
      lang: node.attributes['lang'] as string,
      // `content` is injected by the bundler before transform; the schema
      // accepts it here so the transformer can propagate it through.
      content: node.attributes['content'] as string | undefined,
    });
  },
};

/** Requirement cross-reference.  `id` is a REQ-* identifier. */
const req: Schema = {
  render: 'Req',
  selfClosing: true,
  attributes: {
    id: {
      type: String,
      required: true,
      errorLevel: 'error',
    },
  },
  transform(node, _config) {
    return new Tag('Req', { id: node.attributes['id'] as string });
  },
};

/**
 * Keyboard key sequence.
 *
 * `keys` is a space-separated list of key names (e.g. "Ctrl S", "Mod K").
 * The component renders each key in a <kbd> element.
 */
const kbd: Schema = {
  render: 'Kbd',
  selfClosing: true,
  attributes: {
    keys: {
      type: String,
      required: true,
      errorLevel: 'error',
    },
  },
  transform(node, _config) {
    return new Tag('Kbd', { keys: node.attributes['keys'] as string });
  },
};

/**
 * The complete Markdoc config used by both the bundler and the validator.
 *
 * Keep this export stable -- the content-migration task imports it directly.
 */
export const markdocConfig: Config = {
  tags: {
    callout,
    'code-group': codeGroup,
    include,
    req,
    kbd,
  },
};

/**
 * The set of allowed tag names for the unknown-tag check in the bundler.
 * Derived from the schema so it stays in sync automatically.
 */
export const allowedTags: ReadonlySet<string> = new Set(
  Object.keys(markdocConfig.tags ?? {}),
);
