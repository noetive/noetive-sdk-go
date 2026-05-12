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
	defaultBaseURL = "https://semantik.noetive.io"

	// DefaultNamespace is the namespace the SDK falls back to when
	// [SearchRequest.Namespace], [PublishRequest.Namespace] or
	// [SubscribeRequest.Namespace] is empty. The "global" namespace
	// is provisioned for every account with no extra setup; private
	// namespaces require configuration on the Noetive dashboard and
	// incur usage charges.
	DefaultNamespace = "global"

	// DefaultModel is the embedding model pre-configured for the
	// [DefaultNamespace]. The SDK fills it in when Model is empty
	// and the effective namespace is the default one. Callers using
	// a private namespace must set Model explicitly.
	DefaultModel = "Qwen3-Embedding-4B"

	// DefaultDimensions is the embedding dimensionality pre-configured
	// for the [DefaultNamespace]. The SDK fills it in when Dimensions
	// is zero and the effective namespace is the default one.
	DefaultDimensions uint16 = 1024
)
