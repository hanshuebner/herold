package sesinbound

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hanshuebner/herold/internal/netguard"
)

// S3FetchOutcome is the closed label set for
// herold_hook_ses_s3_fetch_total (REQ-HOOK-SES-05, STANDARDS §7).
type S3FetchOutcome string

const (
	S3FetchOutcomeSuccess          S3FetchOutcome = "success"
	S3FetchOutcomeBucketDisallowed S3FetchOutcome = "bucket_disallowed"
	S3FetchOutcomeS3Error          S3FetchOutcome = "s3_error"
	S3FetchOutcomeHostDisallowed   S3FetchOutcome = "host_disallowed"
)

// s3Fetcher wraps an aws-sdk-go-v2 S3 client with the netguard SSRF
// predicate (REQ-HOOK-SES-06) and an allowlist check.
type s3Fetcher struct {
	client          *s3.Client
	bucketAllowlist []string
}

// newS3Fetcher constructs an S3 client for region using the supplied
// static credentials.  The HTTP transport guards against SSRF by
// refusing dials to RFC 1918 / loopback destinations.
func newS3Fetcher(region, accessKeyID, secretAccessKey, sessionToken string, bucketAllowlist []string) *s3Fetcher {
	return newS3FetcherWithEndpoint(region, accessKeyID, secretAccessKey, sessionToken, bucketAllowlist, "")
}

// newS3FetcherWithEndpoint is like newS3Fetcher but allows overriding the S3
// endpoint URL.  When endpointURL is empty the default AWS endpoint is used.
// Used by unit tests to point the client at a stub server.
func newS3FetcherWithEndpoint(region, accessKeyID, secretAccessKey, sessionToken string, bucketAllowlist []string, endpointURL string) *s3Fetcher {
	var creds aws.CredentialsProvider
	if accessKeyID != "" {
		creds = credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, sessionToken)
	}

	// netguard.ControlContext refuses dials to blocked IP ranges
	// (REQ-HOOK-SES-06) before the TCP handshake.  Unit tests that pass
	// an endpointURL use a plain dialer so localhost test servers are
	// reachable.
	var transport http.RoundTripper
	if endpointURL != "" {
		transport = &http.Transport{}
	} else {
		dialer := &net.Dialer{
			Timeout:        15 * time.Second,
			ControlContext: netguard.ControlContext(),
		}
		transport = &http.Transport{
			DialContext: dialer.DialContext,
		}
	}
	httpClient := &http.Client{
		Timeout:   60 * time.Second,
		Transport: transport,
	}

	cfg := aws.Config{
		Region:     region,
		HTTPClient: httpClient,
	}
	if creds != nil {
		cfg.Credentials = creds
	}

	var opts []func(*s3.Options)
	if endpointURL != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
			o.UsePathStyle = true // path-style for local stub servers
		})
	}

	client := s3.NewFromConfig(cfg, opts...)
	return &s3Fetcher{client: client, bucketAllowlist: bucketAllowlist}
}

// Fetch downloads the S3 object at bucket/key and returns the raw bytes.
// Returns the S3FetchOutcome alongside any error.
func (f *s3Fetcher) Fetch(ctx context.Context, bucket, key string) ([]byte, S3FetchOutcome, error) {
	if !f.bucketAllowed(bucket) {
		return nil, S3FetchOutcomeBucketDisallowed,
			fmt.Errorf("s3fetch: bucket %q not in allowlist", bucket)
	}

	out, err := f.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, S3FetchOutcomeS3Error,
			fmt.Errorf("s3fetch: GetObject %s/%s: %w", bucket, key, err)
	}
	defer out.Body.Close()

	// Cap at 50 MiB to prevent a giant S3 object from OOM-ing the
	// server.  SES receipt actions cap inbound messages at 40 MiB by
	// default; 50 MiB is generous headroom.
	const maxBodyBytes = 50 << 20
	body, err := io.ReadAll(io.LimitReader(out.Body, maxBodyBytes))
	if err != nil {
		return nil, S3FetchOutcomeS3Error,
			fmt.Errorf("s3fetch: read body: %w", err)
	}
	return body, S3FetchOutcomeSuccess, nil
}

func (f *s3Fetcher) bucketAllowed(bucket string) bool {
	for _, b := range f.bucketAllowlist {
		if b == bucket {
			return true
		}
	}
	return false
}
