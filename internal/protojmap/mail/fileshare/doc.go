// Package fileshare implements the JMAP FileShare datatype for the
// attachment-share offload feature (REQ-SHARE-40..44). It provides
// FileShare/get, FileShare/set, FileShare/changes, and FileShare/query
// under the CapabilityFileShares capability
// ("https://netzhansa.com/jmap/file-shares").
//
// The Register function gates capability advertisement behind an
// enabled boolean supplied from sysconfig at boot time; when false no
// methods are registered and the capability does not appear in the
// session descriptor.
package fileshare
