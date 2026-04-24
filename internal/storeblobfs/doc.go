// Package storeblobfs implements the blob store on the local filesystem
// using content-addressed BLAKE3 hex with a 2-level hex fan-out and cross-
// fan-out deduplication.
//
// Ownership: storage-implementor.
package storeblobfs
