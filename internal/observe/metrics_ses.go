package observe

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// SES inbound metrics (REQ-HOOK-SES-05).  Closed-enum label vocabulary
// per STANDARDS §7.  Registered once per process via RegisterSESMetrics.

var (
	sesMetricsOnce sync.Once

	// SESReceivedTotal counts SNS Notification events by final outcome.
	// outcome ∈ {accepted, rejected_signature, rejected_replay,
	//             rejected_topic, rejected_bucket,
	//             rejected_unconfirmed, pipeline_error}
	SESReceivedTotal *prometheus.CounterVec

	// SESSignatureVerifyTotal counts SNS signature-verification attempts
	// by outcome.
	// outcome ∈ {valid, invalid_signature, cert_fetch_failed,
	//             cert_chain_invalid, cert_host_disallowed}
	SESSignatureVerifyTotal *prometheus.CounterVec

	// SESS3FetchTotal counts S3 GetObject calls by outcome.
	// outcome ∈ {success, bucket_disallowed, s3_error, host_disallowed}
	SESS3FetchTotal *prometheus.CounterVec
)

// RegisterSESMetrics registers the three SES inbound metric families;
// safe to call multiple times (idempotent via sync.Once).
func RegisterSESMetrics() {
	sesMetricsOnce.Do(func() {
		SESReceivedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_hook_ses_received_total",
			Help: "SNS Notification events by outcome (accepted | rejected_signature | rejected_replay | rejected_topic | rejected_bucket | rejected_unconfirmed | pipeline_error).",
		}, []string{"outcome"})

		SESSignatureVerifyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_hook_ses_signature_verify_total",
			Help: "SNS signature verification attempts by outcome (valid | invalid_signature | cert_fetch_failed | cert_chain_invalid | cert_host_disallowed).",
		}, []string{"outcome"})

		SESS3FetchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_hook_ses_s3_fetch_total",
			Help: "S3 GetObject calls by outcome (success | bucket_disallowed | s3_error | host_disallowed).",
		}, []string{"outcome"})

		MustRegister(
			SESReceivedTotal,
			SESSignatureVerifyTotal,
			SESS3FetchTotal,
		)
	})
}
