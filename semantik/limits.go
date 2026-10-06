package semantik

// Server-side limits mirrored from the Noetive Semantik public API.
// The SDK's pre-flight validation uses these to fail fast before
// sending a request the server would reject.
const (
	// MaxVectorDim is the largest embedding vector dimensionality
	// the server accepts in a single PublishItem.
	MaxVectorDim = 4096

	// MaxTextBytes is the largest text payload (UTF-8 bytes) the
	// server accepts in a single PublishItem.
	MaxTextBytes = 32 * 1024

	// MaxMetadataKeys is the largest number of metadata keys accepted.
	MaxMetadataKeys = 16

	// MaxMetadataKeyLen is the largest single metadata key length in bytes.
	MaxMetadataKeyLen = 64

	// MaxMetadataValueLen is the largest single metadata value length in bytes.
	MaxMetadataValueLen = 256

	// MaxMetadataTotalBytes is the largest sum of all metadata key and
	// value UTF-8 bytes.
	MaxMetadataTotalBytes = 4 * 1024

	// MaxSearchBodyBytes is the /v1/search body size limit.
	MaxSearchBodyBytes = 1 * 1024 * 1024

	// MaxPublishBodyBytes is the /v1/publish body size limit.
	MaxPublishBodyBytes = 2 * 1024 * 1024

	// MaxSubscribeBodyBytes is the /v1/subscribe body size limit.
	MaxSubscribeBodyBytes = 1 * 1024 * 1024

	// MaxLintBodyBytes is the /v1/lint body size limit.
	MaxLintBodyBytes = 64 * 1024

	// MaxIdempotencyKeyLen is a conservative cap on idempotency keys
	// published by clients.
	MaxIdempotencyKeyLen = 256

	// defaultBaseURL is the production Semantik endpoint.
	//
	// This and the NOETIVE_KEY_SECRET / NOETIVE_SEMANTIK_BASE_URL environment
	// variables are the SDK's entire defaulting surface. The targeting
	// fields — Namespace, Model and Dimensions — are deliberately NOT
	// defaulted: every publish, search and subscribe must set them
	// explicitly, and an unset field fails preflight. Defaulting
	// Namespace to a shared value would let a caller who simply forgot
	// the field route sensitive data into a namespace they never
	// intended, so the SDK fails fast instead.
	defaultBaseURL = "https://semantik.noetive.io"
)
