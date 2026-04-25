// Package searchsnippet implements the JMAP SearchSnippet datatype
// handler per RFC 8621 §6: SearchSnippet/get.
//
// SearchSnippet renders short, term-highlighted excerpts of subject /
// preview text matching an Email/query filter. Backs onto the same
// FTS surface used by Email/query (REQ-PROTO-47): the input filter is
// normalised into a small set of search terms and applied to the
// stored message body / subject, with the matching tokens wrapped in
// <mark>...</mark>.
//
// Bleve's hit-fragmenter is not exposed via the storefts surface yet;
// rather than reach into the index, this package re-derives snippets
// from the message's stored envelope + body blob. The trade-off is one
// extra blob read per requested message, scoped to the principal
// owning the messages so cross-account access is impossible.
package searchsnippet
