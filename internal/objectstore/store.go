// Package objectstore preserves immutable, content-addressed artifact bodies.
package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const MaximumObjectBytes int64 = 4 << 30

var (
	ErrIntegrity   = errors.New("object integrity verification failed")
	ErrUnavailable = errors.New("object store unavailable")
	ErrNotFound    = errors.New("object not found")
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type Authority struct {
	TenantID     string
	RepositoryID string
}

type Descriptor struct {
	SHA256    [sha256.Size]byte
	SizeBytes int64
	MediaType string
}

type Evidence struct {
	ObjectKey              string
	ETag                   string
	VersionID              string
	ProviderChecksumSHA256 string
	Created                bool
}

type Config struct {
	Bucket       string
	Region       string
	BaseEndpoint string
	UsePathStyle bool
}

type Store struct {
	bucket string
	client s3API
}

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.Region) == "" {
		return nil, fmt.Errorf("object store bucket and region are required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load operator object-store configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.BaseEndpoint != "" {
			options.BaseEndpoint = aws.String(cfg.BaseEndpoint)
		}
		options.UsePathStyle = cfg.UsePathStyle
	})
	return newWithClient(cfg.Bucket, client)
}

func newWithClient(bucket string, client s3API) (*Store, error) {
	if strings.TrimSpace(bucket) == "" || client == nil {
		return nil, fmt.Errorf("object store bucket and client are required")
	}
	return &Store{bucket: bucket, client: client}, nil
}

func (s *Store) Publish(
	ctx context.Context,
	authority Authority,
	descriptor Descriptor,
	body io.ReadSeeker,
) (Evidence, error) {
	key, err := validateAndKey(authority, descriptor)
	if err != nil {
		return Evidence{}, err
	}
	if body == nil {
		return Evidence{}, fmt.Errorf("object body is required")
	}
	if err := verifyReader(body, descriptor); err != nil {
		return Evidence{}, err
	}
	digestHex := hex.EncodeToString(descriptor.SHA256[:])
	digestBase64 := base64.StdEncoding.EncodeToString(descriptor.SHA256[:])
	output, putErr := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(s.bucket),
		Key:               aws.String(key),
		Body:              body,
		ContentLength:     aws.Int64(descriptor.SizeBytes),
		ContentType:       aws.String(descriptor.MediaType),
		IfNoneMatch:       aws.String("*"),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(digestBase64),
		Metadata: map[string]string{
			"forja-sha256":     digestHex,
			"forja-size":       strconv.FormatInt(descriptor.SizeBytes, 10),
			"forja-media-type": descriptor.MediaType,
		},
	})
	if putErr != nil && !isPreconditionFailed(putErr) {
		return Evidence{}, fmt.Errorf("%w: publish content-addressed object", ErrUnavailable)
	}
	evidence, err := s.verify(ctx, key, descriptor)
	if err != nil {
		return Evidence{}, err
	}
	evidence.Created = putErr == nil
	if putErr == nil {
		if output.ETag != nil && evidence.ETag != "" && *output.ETag != evidence.ETag {
			return Evidence{}, fmt.Errorf("%w: publication ETag changed before verification", ErrIntegrity)
		}
		if output.VersionId != nil && evidence.VersionID != "" && *output.VersionId != evidence.VersionID {
			return Evidence{}, fmt.Errorf("%w: publication version changed before verification", ErrIntegrity)
		}
	}
	return evidence, nil
}

func (s *Store) Verify(
	ctx context.Context,
	authority Authority,
	descriptor Descriptor,
) (Evidence, error) {
	key, err := validateAndKey(authority, descriptor)
	if err != nil {
		return Evidence{}, err
	}
	return s.verify(ctx, key, descriptor)
}

// ReadVerified returns one bounded, content-addressed body only after the
// provider metadata and bytes match the canonical descriptor. Callers must
// provide the smaller retrieval policy limit; this method never streams an
// unbounded object into a model-facing path.
func (s *Store) ReadVerified(ctx context.Context, authority Authority, descriptor Descriptor, maximumBytes int64) ([]byte, Evidence, error) {
	key, err := validateAndKey(authority, descriptor)
	if err != nil {
		return nil, Evidence{}, err
	}
	if maximumBytes < 1 || descriptor.SizeBytes > maximumBytes {
		return nil, Evidence{}, fmt.Errorf("verified read exceeds caller byte limit")
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		if isNotFound(err) {
			return nil, Evidence{}, ErrNotFound
		}
		return nil, Evidence{}, fmt.Errorf("%w: read content-addressed object", ErrUnavailable)
	}
	if output.Body == nil {
		return nil, Evidence{}, fmt.Errorf("%w: provider returned no body", ErrIntegrity)
	}
	defer output.Body.Close()
	digestHex := hex.EncodeToString(descriptor.SHA256[:])
	if output.ContentLength == nil || *output.ContentLength != descriptor.SizeBytes || output.ContentType == nil || *output.ContentType != descriptor.MediaType || output.Metadata["forja-sha256"] != digestHex || output.Metadata["forja-size"] != strconv.FormatInt(descriptor.SizeBytes, 10) || output.Metadata["forja-media-type"] != descriptor.MediaType {
		return nil, Evidence{}, fmt.Errorf("%w: provider metadata mismatch", ErrIntegrity)
	}
	body, err := io.ReadAll(io.LimitReader(output.Body, maximumBytes+1))
	if err != nil || int64(len(body)) != descriptor.SizeBytes || int64(len(body)) > maximumBytes {
		return nil, Evidence{}, fmt.Errorf("%w: downloaded bytes mismatch", ErrIntegrity)
	}
	digest := sha256.Sum256(body)
	if !equalDigest(digest[:], descriptor.SHA256) {
		return nil, Evidence{}, fmt.Errorf("%w: downloaded bytes mismatch", ErrIntegrity)
	}
	return body, Evidence{ObjectKey: key, ETag: aws.ToString(output.ETag), VersionID: aws.ToString(output.VersionId), ProviderChecksumSHA256: aws.ToString(output.ChecksumSHA256)}, nil
}

func (s *Store) Delete(
	ctx context.Context,
	authority Authority,
	descriptor Descriptor,
	expectedETag string,
	versionID string,
) error {
	key, err := validateAndKey(authority, descriptor)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expectedETag) == "" {
		return fmt.Errorf("expected ETag is required for physical purge")
	}
	input := &s3.DeleteObjectInput{
		Bucket:  aws.String(s.bucket),
		Key:     aws.String(key),
		IfMatch: aws.String(expectedETag),
	}
	if versionID != "" {
		input.VersionId = aws.String(versionID)
	}
	if _, err := s.client.DeleteObject(ctx, input); err != nil {
		if isNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: purge content-addressed object", ErrUnavailable)
	}
	return nil
}

func (s *Store) verify(ctx context.Context, key string, descriptor Descriptor) (Evidence, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(key),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		if isNotFound(err) {
			return Evidence{}, ErrNotFound
		}
		return Evidence{}, fmt.Errorf("%w: read content-addressed object", ErrUnavailable)
	}
	if output.Body == nil {
		return Evidence{}, fmt.Errorf("%w: provider returned no body", ErrIntegrity)
	}
	defer output.Body.Close()
	digestHex := hex.EncodeToString(descriptor.SHA256[:])
	digestBase64 := base64.StdEncoding.EncodeToString(descriptor.SHA256[:])
	if output.ContentLength == nil || *output.ContentLength != descriptor.SizeBytes ||
		output.ContentType == nil || *output.ContentType != descriptor.MediaType ||
		strings.TrimSpace(aws.ToString(output.ETag)) == "" ||
		output.Metadata["forja-sha256"] != digestHex ||
		output.Metadata["forja-size"] != strconv.FormatInt(descriptor.SizeBytes, 10) ||
		output.Metadata["forja-media-type"] != descriptor.MediaType ||
		output.ChecksumSHA256 != nil && *output.ChecksumSHA256 != digestBase64 {
		return Evidence{}, fmt.Errorf("%w: provider metadata mismatch", ErrIntegrity)
	}
	hasher := sha256.New()
	count, copyErr := io.Copy(hasher, io.LimitReader(output.Body, descriptor.SizeBytes+1))
	if copyErr != nil || count != descriptor.SizeBytes || !equalDigest(hasher.Sum(nil), descriptor.SHA256) {
		return Evidence{}, fmt.Errorf("%w: downloaded bytes mismatch", ErrIntegrity)
	}
	return Evidence{
		ObjectKey:              key,
		ETag:                   aws.ToString(output.ETag),
		VersionID:              aws.ToString(output.VersionId),
		ProviderChecksumSHA256: aws.ToString(output.ChecksumSHA256),
	}, nil
}

func validateAndKey(authority Authority, descriptor Descriptor) (string, error) {
	if !uuidPattern.MatchString(authority.TenantID) || !uuidPattern.MatchString(authority.RepositoryID) {
		return "", fmt.Errorf("tenant and repository authority are invalid")
	}
	if descriptor.SizeBytes < 0 || descriptor.SizeBytes > MaximumObjectBytes ||
		len(descriptor.MediaType) < 3 || len(descriptor.MediaType) > 120 ||
		strings.TrimSpace(descriptor.MediaType) != descriptor.MediaType {
		return "", fmt.Errorf("object descriptor is invalid")
	}
	digestHex := hex.EncodeToString(descriptor.SHA256[:])
	return "tenants/" + authority.TenantID +
		"/repositories/" + authority.RepositoryID +
		"/sha256/" + digestHex[:2] + "/" + digestHex[2:], nil
}

func verifyReader(body io.ReadSeeker, descriptor Descriptor) error {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek object body before verification: %w", err)
	}
	hasher := sha256.New()
	count, err := io.Copy(hasher, io.LimitReader(body, descriptor.SizeBytes+1))
	if err != nil || count != descriptor.SizeBytes || !equalDigest(hasher.Sum(nil), descriptor.SHA256) {
		return fmt.Errorf("%w: source bytes do not match declared descriptor", ErrIntegrity)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind verified object body: %w", err)
	}
	return nil
}

func equalDigest(value []byte, expected [sha256.Size]byte) bool {
	return len(value) == sha256.Size && string(value) == string(expected[:])
}

func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) &&
		(apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "ConditionalRequestConflict")
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) &&
		(apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")
}
