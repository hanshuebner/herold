// Package categorysettings implements the JMAP CategorySettings singleton
// datatype (REQ-CAT-50 / https://netzhansa.com/jmap/categorise).
//
// CategorySettings is a per-principal singleton that exposes the active
// LLM category set and classifier prompt to JMAP clients. The suite reads
// it on bootstrap to populate the inbox tab configuration; it writes it
// via CategorySettings/set when the user edits the category set or prompt.
// CategorySettings/recategorise enqueues a background job that re-runs
// the categoriser on the principal's inbox messages.
//
// Storage lives in the existing jmap_categorisation_config table (migration
// 0009). The JMAP state counter lives in jmap_states.category_settings_state
// (migration 0025). No separate storage table is introduced.
package categorysettings
