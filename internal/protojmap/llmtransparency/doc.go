// Package llmtransparency implements the G14 LLM-transparency JMAP surface
// (REQ-FILT-65, REQ-FILT-66, REQ-FILT-67, REQ-FILT-68, REQ-FILT-216).
//
// The package registers two JMAP methods under the
// "https://netzhansa.com/jmap/llm-transparency" capability:
//
//   - LLMTransparency/get — returns the per-account singleton describing the
//     user-visible spam and categorisation prompts currently in effect, the
//     category set, model identifiers, and a disclosure note. Operator
//     guardrails are explicitly excluded (REQ-FILT-67).
//
//   - Email/llmInspect — returns per-message classification detail for a list
//     of Email IDs: spam verdict/confidence/reason/prompt-as-applied/model and
//     categorisation assignment/prompt-as-applied/model. Again, guardrails are
//     excluded and the body excerpt is not re-exposed.
//
// Register is the package entry point; call it once at server startup.
package llmtransparency
