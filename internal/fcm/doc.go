// Package fcm implements the outbound Firebase Cloud Messaging HTTP
// v1 transport the push gateway's FCM subscriptions use (re #200).
// It is a delivery backend for the webpush.Dispatcher's existing
// change-feed-driven fan-out — rule evaluation, enriched-vs-minimal
// payload selection, and per-(subscription, thread) coalescing all
// live in package webpush unchanged; this package only turns a built
// payload into an authenticated FCM HTTP v1 request.
//
//   - sender.go — Sender: mints (or reuses, via TokenSource) a
//     service-account OAuth2 bearer token and POSTs a data-only
//     message to https://fcm.googleapis.com/v1/projects/<project>/
//     messages:send.
//
// The message is data-only (no "notification" key) so the Android
// FirebaseMessagingService always runs, including when the app is
// backgrounded, matching the architecture note in
// docs/design/android/architecture/04-push.md: the app itself posts
// the notification after a bounded reconcile pass, rather than the OS
// tray auto-rendering a payload the app never sees.
package fcm
